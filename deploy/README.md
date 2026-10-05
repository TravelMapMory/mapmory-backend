# Deployment

Every merge to `main` in `mapmory-backend` or `mapmory-frontend` ships to
**https://mapmory.app** within about two minutes. Nobody deploys by hand.

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
| VM identity | `mapmory-vm@…` (read `mapmory`, write logs and metrics) |

The WIF provider accepts a GitHub token only if all of these hold: the org is
TravelMapMory (`repository_owner_id 331570017`), the repo is one of these two
(`repository_id` 1378158602 / 1378158659), the ref is `refs/heads/main` and
the event is `push`. A deploy for a commit that is no longer `main`'s tip (a
slow or re-run job) pushes `:<sha>` but leaves `:main` alone.

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

A container is only recreated when its image changes, so if you changed the
`docker run` flags in `update.sh`, also run `sudo docker rm -f <name>` on the VM;
the timer starts it again with the new flags within a minute.

Costs run on the free-trial credit (ends 2027-01-04): roughly €10 a month for
the VM, IP and disk. A €30/month budget alert emails the billing admin.
