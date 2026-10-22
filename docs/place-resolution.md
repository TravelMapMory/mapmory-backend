# Place resolution

`internal/place` turns photo coordinates into normalized place labels (country, city, place name) for [backend #20](https://github.com/TravelMapMory/mapmory-backend/issues/20). It is a library: nothing in `cmd/server` calls it yet.

- `place.Provider` is the one-method interface a geocoding vendor implements.
- `place.Resolver` validates coordinates, calls the provider and normalizes the result into a `place.ResolvedPlace`.
- `internal/place/geoapify` is the first provider. Its request and response types are private to that package.

## Configuration

The Geoapify adapter takes its key from `geoapify.Config`; it never reads the environment itself. The server wiring is expected to do:

```go
geoapify.New(geoapify.Config{APIKey: os.Getenv("GEOAPIFY_API_KEY")})
```

- `GEOAPIFY_API_KEY` is a server-side secret. Never log it, never send it to the frontend.
- The request URL carries the key as a query parameter, so never log the URL. The adapter keeps the URL and the response body out of its errors.
- `Timeout` covers one whole request and defaults to 10 seconds. `BaseURL` exists only so tests can use `httptest`.

## Errors

Callers branch with `errors.Is`:

| Error | Meaning | Caller action |
| --- | --- | --- |
| `ErrInvalidCoordinates` | Latitude or longitude is NaN, infinite or out of range | Reject the input (400 at the API) |
| `ErrNoResult` | The provider had nothing usable | Keep the coordinates, leave the labels empty |
| `ErrTransient` | Timeout, network failure, HTTP 408, 429 or 5xx, canceled context | Retry later |
| `ErrPermanent` | Other 4xx (bad key, bad request) or an unreadable response | Do not retry; fix configuration |

A photo without coordinates never reaches the resolver: the caller skips `Resolve` and the photo stays awaiting a location. A result with only some labels is not an error; the missing levels are empty strings.

## Attribution

Geoapify's terms require a "Powered by Geoapify" link, and OpenStreetMap data requires its own credit. Every `ResolvedPlace` carries both in `Attributions`, plus `Provider` (`"geoapify"`). When Geoapify answers from another data source (for example Who's On First), that source's credit is added as a third entry. Show them next to the labels. Storing labels is allowed as long as the attribution stays with them, so persist `Provider` and `Attributions` alongside the labels.

## Adding a provider

Create `internal/place/<name>` with a type that implements `place.Provider`: map the vendor's failures to the four errors above, set `Provider` and `Attributions`, and keep keys and coordinates out of errors. Choose the provider at wiring time with `place.NewResolver(p)`. There is no registry; one constructor call is the registration.

## Fallback versus parallel fan-out

Neither is implemented. They differ as follows.

- **Fallback** (call B only when A returns `ErrTransient` or `ErrNoResult`):
  - Cost and quota are about one call per photo.
  - Attribution is that of whichever provider answered.
  - There are no conflicting answers.
- **Parallel fan-out** (call every provider at once):
  - N providers means N times the cost and quota (the Geoapify free plan allows 3000 credits per day).
  - Latency is the slowest call, or the fastest if the others are canceled.
  - Every provider whose data is shown must be credited.
  - Conflicting labels need a rule (priority order or field-level merge), and stored labels must record which provider supplied them.

## Deferred

The owner-authorized location-correction endpoint (#18 contract), persisting effective coordinates and labels apart from the original EXIF coordinates, worker wiring (#22, #23), handling photos without coordinates (the worker skips `Resolve` and the photo stays `needs-location`) and its test, reading the key in `cmd/server`, additional providers, fan-out, automatic fallback, ranking and caching.
