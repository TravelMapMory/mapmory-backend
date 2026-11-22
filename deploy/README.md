# Deployment

The configured CI and VM updater publish `main` images for `mapmory-backend` and `mapmory-frontend` to **https://mapmory.app**. The VM checks for new images approximately every minute; deployment is not equivalent to enabling database mode or validating the complete photo-sharing flow.

## How a release happens

1. The `deploy` job in `.github/workflows/ci.yml` runs after `build`, on pushes
   to `main` only. It signs in to Google Cloud without keys (Workload Identity
   Federation) and pushes the image as `:<commit sha>` and `:main` to Artifact
   Registry: `europe-north1-docker.pkg.dev/mapmory-2026/mapmory/{api,web}`.
2. The VM `mapmory-web` runs [`cloud-init.yaml`](cloud-init.yaml). A systemd
   timer checks for a new `:main` every minute and restarts only the container
   whose image changed.
3. Caddy on the VM terminates HTTPS for `mapmory.app` (Let's Encrypt) and
   routes `/api/*` to the Go API and everything else to the nginx container.

## Why a VM and not Cloud Run

The project lives in Aalto's Google Cloud organisation (`aalto.fi / Sandbox`),
whose policies make every public option except a VM impossible:

- `run.managed.requireInvokerIam` is enforced and `iam.allowedPolicyMemberDomains`
  only allows Aalto accounts, so a Cloud Run service cannot be made public.
- `compute.restrictProtocolForwardingCreationForTypes` allows only `INTERNAL`,
  so there can be no external load balancer in front of Cloud Run either.

The images are ordinary containers, so moving to Cloud Run later (for example
in a project outside the Aalto organisation) only changes where they run.

## Resources (project `mapmory-2026`, region `europe-north1`)

| What | Name |
|---|---|
| VM (e2-micro, Container-Optimized OS) | `mapmory-web`, zone `europe-north1-a` |
| Static IP | `mapmory-ip` = `34.88.36.74` |
| Network / firewall | `mapmory-net`; `mapmory-allow-web` (80, 443), `mapmory-allow-iap-ssh` (22 from IAP only) |
| Images | Artifact Registry repo `mapmory` |
| CI identity | `mapmory-deployer@…` (push to `mapmory` only), WIF pool `github` / provider `github-oidc` |
| VM identity | `mapmory-vm@…` (read `mapmory`, write logs and metrics; object user on the photo bucket; signs its own URLs) |
| Photo bucket | `mapmory-2026-photos`: private (uniform access, public access prevention), CORS `PUT` from `http://localhost:5173` and `https://mapmory.app` |

The WIF provider accepts a GitHub token only if all of these hold: the org is
TravelMapMory (`repository_owner_id 331570017`), the repo is one of these two
(`repository_id` 1378158602 / 1378158659), the ref is `refs/heads/main` and
the event is `push`. A deploy for a commit that is no longer `main`'s tip (a
slow or re-run job) pushes `:<sha>` but leaves `:main` alone.

## API configuration

`update.sh` in [`cloud-init.yaml`](cloud-init.yaml) passes the API its environment:

| Variable | Value | Why |
|---|---|---|
| `FIREBASE_PROJECT_ID` | `mapmory-development` | The API verifies Firebase ID tokens for this project and refuses to start without it |
| `MAPMORY_PHOTO_BUCKET` | `mapmory-2026-photos` | Private bucket for photo originals and display images; enables the upload and photo routes in database mode |

`MAPMORY_UPLOAD_SIGNER` stays unset on the VM: the API detects `mapmory-vm` from
the metadata server, uses it for storage and signs through the IAM Credentials API. Upload routes
exist only in database mode.

### Database mode

`update.sh` also reads the Secret Manager secret `mapmory-database-url` every
minute. When `mapmory-vm` can read it, the API gets
`MAPMORY_ENABLE_DB_USER_RESOLUTION=true` and that `DATABASE_URL`, through a
root-only env file on tmpfs (`/run/mapmory/api.env`), never the command line.
When the secret is missing or not readable (`404`/`403`), the API runs the public
in-memory prototype, which is the state today. A failed fetch (timeout, `5xx`)
keeps the current mode, so an outage never drops the API back to the prototype.
The API container is recreated within a minute whenever that file changes.

Turning it on, in this order ([#53](https://github.com/TravelMapMory/mapmory-backend/issues/53)):

1. Apply this `cloud-init.yaml` to the VM once (see *Change the host setup*). Without the secret nothing else changes.
2. Apply the Goose migrations to the hosted database ([#42](https://github.com/TravelMapMory/mapmory-backend/issues/42)). The API never migrates.
3. Store the connection string and let only the VM read it:

   ```bash
   gcloud services enable secretmanager.googleapis.com --project mapmory-2026
   printf '%s' "$DATABASE_URL" | gcloud secrets create mapmory-database-url \
     --project mapmory-2026 --replication-policy automatic --data-file -
   gcloud secrets add-iam-policy-binding mapmory-database-url --project mapmory-2026 \
     --member serviceAccount:mapmory-vm@mapmory-2026.iam.gserviceaccount.com \
     --role roles/secretmanager.secretAccessor
   ```

   The VM's access scopes must include `cloud-platform`, which signing upload URLs
   through the IAM Credentials API needs as well. Check with
   `gcloud compute instances describe mapmory-web --zone europe-north1-a --project mapmory-2026 --format='value(serviceAccounts[0].scopes)'`.

4. Check that signed-out `curl -i https://mapmory.app/api/trips` now answers `401`.
5. Only then set the frontend's Actions variables `VITE_API_URL=https://mapmory.app`
   and `VITE_FIREBASE_API_KEY`, `VITE_FIREBASE_AUTH_DOMAIN`, `VITE_FIREBASE_PROJECT_ID`,
   `VITE_FIREBASE_APP_ID` (the web app of the project in `FIREBASE_PROJECT_ID`, with
   `mapmory.app` in its Authorized domains), and re-run the frontend's latest `main`
   deploy. Unset, the frontend image keeps the mock data and the demo sign-in.

To go back to the prototype, unset the frontend variables and redeploy it first,
then remove the VM's access to the secret.

The Goose schema must include migration `00007_create_shares.sql` before the API runs in this mode. `GEOAPIFY_API_KEY` is optional (suggested place labels and place search) and stays server-side.

After turning it on, verify with two Firebase test accounts: owner isolation, a real upload and display image, share creation, anonymous recipient access, and revocation. Newly revoked shares reject subsequent API lookups, but already issued signed GCS URLs can remain accessible until they expire. **Never** run migration rollback on the deployed database.

No service-account key is needed. Token verification only fetches Google's public
certificates, and the Firebase Admin SDK's startup credential lookup is satisfied by
the VM's own identity (`mapmory-vm`) through the metadata server.

A backend run on a laptop acts as `MAPMORY_UPLOAD_SIGNER` (`mapmory-vm`) for
the bucket and for signing, impersonating it through the developer's own ADC
(`gcloud auth application-default login` with an Aalto account), without a key.
The developer needs no access to the bucket itself. This needs Token Creator on `mapmory-vm`, which an
Owner grants per person:

```bash
gcloud iam service-accounts add-iam-policy-binding \
  mapmory-vm@mapmory-2026.iam.gserviceaccount.com --project mapmory-2026 \
  --member user:<name>@aalto.fi --role roles/iam.serviceAccountTokenCreator
```

To change a value, edit `cloud-init.yaml` and apply it as described under *Change the
host setup*. The container is recreated with the new value within a minute.

## DNS (Porkbun)

| Type | Host | Value |
|---|---|---|
| A | `mapmory.app` | `34.88.36.74` |
| CNAME | `www` | `mapmory.app` |

Delete Porkbun's parking records (`ALIAS` and `CNAME *` to `pixie.porkbun.com`).
Caddy waits until the VM is the only A record for `mapmory.app` before it asks
Let's Encrypt for a certificate, so the order of DNS and VM changes does not matter.

## Common tasks

Roll back by pointing `:main` at an earlier commit; the VM follows within a minute:

```bash
gcloud artifacts docker tags add \
  europe-north1-docker.pkg.dev/mapmory-2026/mapmory/web:<good sha> \
  europe-north1-docker.pkg.dev/mapmory-2026/mapmory/web:main
```

Look at the host (SSH goes through IAP; the VM has no open SSH port):

```bash
gcloud compute ssh mapmory-web --zone europe-north1-a --project mapmory-2026 --tunnel-through-iap
sudo docker ps
sudo journalctl -u mapmory-update.service -n 50
```

Change the host setup: edit `cloud-init.yaml`, then

```bash
gcloud compute instances add-metadata mapmory-web --zone europe-north1-a \
  --project mapmory-2026 --metadata-from-file user-data=deploy/cloud-init.yaml
gcloud compute instances reset mapmory-web --zone europe-north1-a --project mapmory-2026
```

A container is recreated when its image or its `docker run` arguments change
(they are stored in the `mapmory.run` label), so no `docker rm` is needed.

Costs run on the free-trial credit (ends 2027-01-04): roughly €10 a month for
the VM, IP and disk. A €30/month budget alert emails the billing admin.
