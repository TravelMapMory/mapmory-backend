# Photo processing module

See also the [README](../README.md) and [photo metadata inspection](photo-metadata.md).

`internal/photoproc` is the standalone component behind the EXIF extraction and display-image work in [backend #22](https://github.com/TravelMapMory/mapmory-backend/issues/22). It works on bytes already in memory, so it needs no HTTP server, cloud credentials, database or geocoder, and it is not called by any endpoint yet. Reading the uploaded object from Google Cloud Storage, saving the `Photo` record and wiring this into upload completion belong to [#12](https://github.com/TravelMapMory/mapmory-backend/issues/12), [#21](https://github.com/TravelMapMory/mapmory-backend/issues/21) and [#23](https://github.com/TravelMapMory/mapmory-backend/issues/23).

### Entry points

| Name | Purpose |
| --- | --- |
| `photoproc.DisplayJPEG(data []byte) ([]byte, error)` | Validates the image and returns the display JPEG |
| `photoproc.ExtractMetadata(data []byte) (Metadata, error)` | Returns GPS, capture time and UTC offset, each marked `missing`, `malformed` or `usable` |
| `photoproc.MaxInputBytes` | Largest accepted upload, 25,000,000 bytes (the OpenAPI limit) |
| `(*photoproc.Error).Code` | Reason an image was refused; safe to store as `Photo.error_code` |

### Input and output

- Input: JPEG or PNG. The format is read from the file's magic bytes, never from the file name or `Content-Type`.
- Output: a baseline JPEG at quality 85. Only pixels are written, so the output carries no EXIF, XMP, ICC profile or GPS data. The input slice is only read; the original is never modified, and the worker must store the display image as a separate object.
- The longest side is at most 2048 px and smaller images are never upscaled. Scaling uses Catmull-Rom (`draw.CatmullRom.Transform`) from `golang.org/x/image`, a pure-Go package, so the `CGO_ENABLED=0` build still works.
- The EXIF orientation is applied to the pixels, so the output is upright without the tag.
- Transparency is flattened onto white, because JPEG has no alpha channel.

### Limits and errors

| Condition | `Error.Code` |
| --- | --- |
| More than 25,000,000 bytes | `file_too_large` |
| Not a JPEG or PNG (for example GIF, WebP, HEIC or text), or a JPEG that Go's decoder does not implement (arithmetic coding, lossless, 12-bit, or a rare chroma subsampling) | `unsupported_type` |
| More than 50 megapixels, or more than the decode budget for its kind of image (see [Resource use](#resource-use)); both checked from the header before any pixels are decoded | `image_too_large` |
| Zero width or height, or header or pixel data that cannot be decoded, including truncated files | `invalid_image` |

`Error.Err` holds the cause for logs and should not be shown to users. Any error from `DisplayJPEG` that is not an `*Error` is internal (for example an encoder failure) and should be treated as a server error.

#### Resource use

Memory is the constraint: the API runs on a GCE e2-micro VM whose 1 GB is shared with Caddy, nginx and later Postgres. Before decoding, `DisplayJPEG` estimates from the header how much memory the standard-library decoder will hold, and refuses the image as `image_too_large` when

```text
width x height x bytes per pixel > 200,000,000
```

| Input | Bytes per pixel while decoding | Largest accepted |
| --- | --- | --- |
| Baseline JPEG, YCbCr / grayscale | 3 / 1 | 50 MP (the pixel limit) |
| Baseline JPEG, RGB / CMYK | 7 / 8 | 28 MP / 25 MP |
| Progressive JPEG, YCbCr | 15 | 13.3 MP |
| Progressive JPEG, grayscale / RGB / CMYK | 5 / 19 / 24 | 40 MP / 10.5 MP / 8.3 MP |
| 8-bit PNG | 4 | 50 MP (the pixel limit) |
| 16-bit PNG, or interlaced 8-bit PNG | 8 | 25 MP |
| Interlaced 16-bit PNG | 16 | 12.5 MP |

The decoder holds one byte per sample, and YCbCr is counted as 4:4:4 because the subsampling is not checked. A progressive JPEG also keeps every DCT coefficient (4 bytes per sample), an RGB or CMYK JPEG is converted into a second image of 4 bytes per pixel, and an interlaced PNG is decoded pass by pass into a second image. A JPEG with an Adobe marker whose transform is 0 is converted from YCbCr to RGB at the end of the decode, so it counts as RGB even when the marker sits after the first scan, where the header read does not look. Phone cameras write baseline YCbCr JPEGs, so their photos are accepted up to 50 MP.

Scaling adds no buffer proportional to the source: `Kernel.Transform` produces exactly the pixels of `CatmullRom.Scale`, which allocated 2048 x the source height x 32 bytes (about 200 MB for a 12 MP photo). Peak memory of one call is therefore about

```text
peak = input bytes + decode (above) + 2 x display image (up to 16 MiB each) + about 8 MiB
```

which stays under about 265 MiB with a 25 MB input, counting the output JPEG while it is encoded (up to ~4 MiB) and a detailed scaled image. The display image is 12 MiB for a 4:3 photo and 16 MiB for a square one. The second display image is the rotated copy, made only when the EXIF orientation is 2 to 8.

Peak RSS of one `DisplayJPEG` call in a fresh process, measured with `/usr/bin/time -l` on a development laptop (Apple M4 Pro) with synthetic photos (indicative, not a benchmark). The JPEGs carry EXIF orientation 6, so the rotated copy is included:

| Input | Peak RSS | Time |
| --- | --- | --- |
| 12 MP baseline JPEG, 4:2:0 (2 MB) | 50 MiB | 1.0 s |
| 50 MP baseline JPEG, 4:2:0 (7.5 MB) | 110 MiB | 3.1 s |
| 50 MP baseline JPEG, 4:4:4 (23 MB) | 197 MiB | 3.5 s |
| 13.3 MP progressive JPEG, 4:4:4 (2.6 MB) | 222 MiB | 1.1 s |
| 8.3 MP progressive CMYK JPEG (2.3 MB) | 222 MiB | 1.2 s |
| 25 MP baseline CMYK JPEG (6 MB) | 226 MiB | 2.8 s |
| 25 MP 16-bit RGBA PNG, not rotated (0.3 MB) | 208 MiB | 2.7 s |
| 12.5 MP interlaced 16-bit RGBA PNG, not rotated (0.1 MB) | 197 MiB | 1.5 s |
| 50 MP 8-bit RGBA PNG, square, rotated, padded to 24.9 MB (the worst case) | 254 MiB | 2.4 s |
| Any image over the limits | input size + about 5 MiB | under 1 ms |

Bytes after the end of the image still count toward the 25 MB input, so a hostile file can pad itself to the limit; the worst-case row is padded for that reason. The other kinds that fill the decode budget (progressive 4:4:4, progressive and baseline CMYK, 16-bit and interlaced PNG) peaked at 232 to 244 MiB when padded to 24.9 MB.

Most of the time is the single-threaded Catmull-Rom scaling. The e2-micro's shared core is slower than the laptop, so expect several times longer there.

`DisplayJPEG` returns its memory to the operating system before it returns (`debug.FreeOSMemory`, 10 to 50 ms). Without that, the garbage collector let back-to-back calls pile up: three worst-case images in a row reached 660 MiB RSS without it, and eight stayed at 255 MiB with it.

**Integration requirement:** run at most one `DisplayJPEG` at a time, for example behind a semaphore of size 1 shared by every request that processes uploads. One call can hold about 265 MiB, so two at once do not fit next to Caddy, nginx and Postgres.

### Metadata rules

- Every field is `missing` (absent, blank or zeroed), `malformed` (present but unusable) or `usable`. `ExtractMetadata` sets a value field only for `usable`, so map the other two states to `null`.
- GPS: both coordinates are range-checked (latitude within 90, longitude within 180) and set together or not at all. A zero denominator, a bad hemisphere letter or an out-of-range value is `malformed`; a file with no GPS tags is `missing`.
- `CaptureTimeLocal` is the camera's wall-clock time in the form `2025-07-14T09:30:00`, matching `capture_time_local`. It carries no zone and is never treated as UTC.
- `CaptureUTCOffset` (`+hh:mm`) is only what the file states, from `OffsetTimeOriginal`. When it is absent it stays `missing`; no offset is guessed from the GPS position or the server's time zone. Time and offset are judged independently.
- Malformed EXIF is never fatal. A damaged EXIF block only marks the fields `malformed` and adds a line to `Diagnostics`, and `DisplayJPEG` still produces a display image, just unrotated.
- `Diagnostics` holds one human-readable line per missing or malformed field, for logs.

### How the worker should use it

```go
data, err := io.ReadAll(io.LimitReader(obj, photoproc.MaxInputBytes+1))
// A read error is transient: retry or return 500, never persist it as failed.

display, err := photoproc.DisplayJPEG(data)
var perr *photoproc.Error
if errors.As(err, &perr) {
    // Persist state=failed with error_code=perr.Code.
} else if err != nil {
    // Internal failure: return 500.
}

// Cannot fail once DisplayJPEG succeeded: both accept a JPEG by its FF D8
// signature and a PNG by its 8-byte signature.
meta, _ := photoproc.ExtractMetadata(data)
```

Then:

1. Upload `display` as a new object next to the original; never overwrite the original.
2. State is `ready` when `meta.GPSStatus == photoproc.Usable`, otherwise `needs-location`. An image with no GPS or no capture time still gets a display image.
3. `exif_lat` and `exif_lng` come from `meta.GPS` (both `null` when it is nil).
4. `capture_time_local` and `capture_utc_offset` are `null` unless their status is `usable`.
5. Log `meta.Diagnostics`.

Not done here and left for the integration work: running one `DisplayJPEG` at a time (required, see [Resource use](#resource-use)), adding an `error_code` enum (`unsupported_type`, `file_too_large`, `image_too_large`, `invalid_image`) to the OpenAPI contract after team review, and checking the GCS object size before downloading it (#21).

### Limitations

- No HEIC, WebP or GIF input, and no animation.
- The ICC profile is dropped, so Display-P3 phone photos appear as sRGB and slightly less saturated.
- Only `DateTimeOriginal` and `OffsetTimeOriginal` are read; there is no fallback to `OffsetTime` or the GPS timestamp.
- One display size only; there is no thumbnail.
- The whole file and the decoded image are held in memory, so progressive, RGB and CMYK JPEGs and 16-bit or interlaced PNGs have lower size limits than baseline YCbCr JPEGs (see [Resource use](#resource-use)).
- An orientation value outside 1-8 displays the image as stored.
- `Kernel.Transform` takes about four times as long as `CatmullRom.Scale` for the scaling step, and 2 to 3 times as long end to end (0.95 s instead of 0.35 s for a 12 MP JPEG), in exchange for having no scaler buffer. If CPU time on the VM becomes the bottleneck, the upgrade is a row-streaming separable Catmull-Rom: measured at 0.25 s for 12 MP with the same pixels to within rounding, but about 70 lines of resampling code. Bilinear and box filters were measured too and alias visibly on fine patterns.

