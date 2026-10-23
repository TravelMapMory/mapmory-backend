# Firebase Authentication — local development

**Project:** `mapmory-development`  
**Authentication provider:** Firebase Email/Password  
**Local backend credentials:** keyless Google Cloud service-account impersonation (no private-key downloads)

> **Current state:** Firebase ID-token verification is implemented at `/api/auth/me`. The optional `/api/users/me` endpoint also resolves the verified UID to a PostgreSQL application user; see [Application-user resolution](application-user-resolution.md). Prototype trip routes remain unprotected and in-memory.

For manual sign-in and automated verification, see [Firebase Authentication — testing](firebase-auth-testing.md).

## 1. The simplest mental model

Every developer runs their **own React frontend and Go backend**, but everyone uses the **same Firebase Authentication project**, `mapmory-development`.

There are **two independent forms of authentication**:

| Question | Answer | Credential |
| --- | --- | --- |
| **Who is using MapMory?** | Firebase signs in an application user with email/password. | **Firebase ID token**; sent by React to the Go API as `Authorization: Bearer ...` |
| **Who is the local backend when calling Google APIs?** | Google Cloud allows a developer's local Go process to act as the `mapmory-local-dev` service account. | **Google OAuth access token**; obtained via Application Default Credentials (ADC) |

```text
APPLICATION USER SIGN-IN                  DEVELOPER / BACKEND IDENTITY

MapMory user                              Kristian's own Google account
   | email + password                        | Google Cloud IAM permits impersonation
   v                                         v
Firebase Authentication                 mapmory-local-dev service account
(project: mapmory-development)               |
   | Firebase ID token                       | gcloud configures keyless ADC locally
   v                                         v
React -> Local Go API                    Local Go process
   | Admin SDK verifies ID token             | ADC obtains short-lived OAuth tokens
   | using Google's public signing keys      | representing mapmory-local-dev
   |                                         |
   v                                         v (only when authorized calls are needed)
Trusted Firebase UID                    Google APIs
                                        (including Firebase management APIs)
```

**The tokens are not interchangeable.** A Google OAuth access token does **not** log a user into MapMory, and a Firebase ID token does **not** grant Google Cloud IAM permissions.

### How the Google service account is related to Firebase

The Firebase project **is also a Google Cloud project** with ID `mapmory-development`. Its Firebase Auth users are managed through Firebase; its service accounts and IAM permissions are managed through Google Cloud.

The service account `mapmory-local-dev@mapmory-development.iam.gserviceaccount.com` belongs to that project. Granting a developer **Service Account Token Creator** on that account means the developer may request short-lived Google credentials **as that service account**. It does not create Firebase users or Firebase ID tokens, and it does not grant Firebase Console access.

The Firebase **Admin SDK** can use these Google credentials for authorized Firebase/Google API operations. **Ordinary Firebase ID-token verification primarily uses Google's public signing keys and validates the token's issuer, audience, and expiry**; the service account is not what signs those user tokens. We are standardizing on keyless ADC for the server's Google identity and possible future Admin SDK operations, not because impersonation itself generates user tokens.

## 2. Who needs what?

| Team activity | What is required |
| --- | --- |
| Implement React email/password sign-in | Firebase Web app configuration (nonsecret); **no Google IAM impersonation or Firebase Console access** |
| Sign in as a MapMory test user | A test user in the shared Firebase project (or an enabled user-registration flow); no IAM role |
| Run the local Go backend with the team's keyless ADC setup | Google account with **Service Account Token Creator** on `mapmory-local-dev`; follow Section 3 |
| Unit-test HTTP middleware with fake verifiers | No Google credentials required |
| Change Firebase provider settings / manage accounts in Console | A **separate** appropriate Firebase/Google Cloud IAM project role |
| Grant developer impersonation access | Permission to edit **the service account's IAM policy** |

**Current team access:** Ki Chun is currently the only project administrator. Kristian and Kane do not need to be Firebase Console administrators to develop and test sign-in. Each person needs their **own** Google identity and local setup for backend impersonation; do not share Google passwords, ADC files, OAuth tokens, or private keys.

## 3. For Kristian and Kane — run a local backend with real Firebase

### Step A — Request access (once)

Send Ki Chun the email address of **your own Google account**, the one you will use with `gcloud`. Ask for `roles/iam.serviceAccountTokenCreator` **on this service account only**:

`mapmory-local-dev@mapmory-development.iam.gserviceaccount.com`

Suggested message:

> Could you give my Google account (`YOUR_EMAIL`) permission to impersonate `mapmory-local-dev` for local backend development? I only need Service Account Token Creator on that service account, not Firebase Console access.

Wait for confirmation. See [Administrator access management](#6-for-the-administrator--grant-or-revoke-teammate-access) below.

### Step B — Configure Google Cloud CLI (once per laptop)

Install [Google Cloud CLI](https://cloud.google.com/sdk/docs/install), then run:

```bash
gcloud auth login
gcloud config set project mapmory-development

# Do not override ADC with a downloaded private-key file.
unset GOOGLE_APPLICATION_CREDENTIALS

gcloud auth application-default login \
  --impersonate-service-account=mapmory-local-dev@mapmory-development.iam.gserviceaccount.com
```

A successful login says that credentials were **saved**. This is just local configuration; it does **not** yet prove you can impersonate the service account.

### Step C — Verify you can obtain a token

```bash
gcloud auth application-default print-access-token >/dev/null \
  && echo 'ADC impersonation works'
```

If you see `ADC impersonation works`, Google Cloud accepted your identity and allowed you to obtain a short-lived OAuth access token for `mapmory-local-dev`. The Go client libraries can refresh it automatically. This **does not verify a Firebase user ID token**. For a real-token smoke test, see [Firebase Authentication — testing](firebase-auth-testing.md).

Do **not** print or share the access token. Don't copy your local `~/.config/gcloud/application_default_credentials.json` to teammates; each person generates their own ADC configuration.

### Step D — Point the Go backend at the Firebase project

In the shell where you launch MapMory:

```bash
export FIREBASE_PROJECT_ID=mapmory-development
unset GOOGLE_APPLICATION_CREDENTIALS

go run ./cmd/server
```

**Important:** The backend initializes the Firebase Admin SDK at startup. From the repository root, it reads `FIREBASE_PROJECT_ID` and the other two server configuration keys from an optional `.env`; explicitly exported values take precedence. `gcloud` ADC is discovered through Google’s credential chain. The trip endpoints remain unprotected.

The React frontend separately needs the registered **Firebase Web app configuration** (`apiKey`, `authDomain`, `projectId`, `appId`, etc.), provided by the project administrator. These identifiers are not service-account credentials. See the frontend's integration issue for wiring them into the Firebase Web SDK.

## 4. Why we use keyless credentials

The old alternative is downloading a **service-account private-key JSON file**. We **do not use that approach**. A private key is long-lived and can be copied or accidentally committed.

Instead, Google Cloud CLI stores a *local impersonation configuration*. Your own Google login allows Google Cloud to issue temporary credentials as the named service account. **No service-account private key is downloaded.** Developers can be removed individually from the IAM policy.

Do not generate, share, commit, or deploy service-account private keys. Never commit ADC files, OAuth access tokens, Firebase ID tokens, or test-user passwords. Deployed Google Cloud services should use their attached runtime identity rather than your local impersonation credentials.

## 5. Troubleshooting

| Symptom | Meaning / fix |
| --- | --- |
| ADC login said “Credentials saved,” but token test fails with `iam.serviceAccounts.getAccessToken denied` | You lack Token Creator on the target account, or the browser login used a different Google account. Request/check grant, then retry after IAM propagation. |
| `NOT_FOUND` for `mapmory-local-dev` | Confirm project and service-account email with administrator. |
| `gcloud auth application-default set-quota-project` says “not user credentials” | Expected for the impersonated ADC type. **Skip this command**; it is not a required setup step. |
| Frontend developer cannot open Firebase Console | Normal if no separate Console role has been granted; the frontend can use the shared Web SDK config. |
| OAuth token test passes, but Firebase middleware test fails | Different tests. Check the Firebase Admin SDK initialization, actual Firebase **ID token**, configured project ID, and whether extra API permissions are required. |

## 6. For the administrator — grant or revoke teammate access

The following **does not happen inside Firebase Authentication**. IAM permissions are managed for the *same underlying Google Cloud project* `mapmory-development`.

**Console:** [Google Cloud → Service Accounts](https://console.cloud.google.com/iam-admin/serviceaccounts?project=mapmory-development) → select `mapmory-local-dev` → **Permissions → Grant access** → enter the teammate's Google account email → select **Service Account Token Creator** → Save.

**Equivalent CLI:**

```bash
gcloud iam service-accounts add-iam-policy-binding \
  mapmory-local-dev@mapmory-development.iam.gserviceaccount.com \
  --project=mapmory-development \
  --member='user:TEAMMATE_GOOGLE_EMAIL' \
  --role='roles/iam.serviceAccountTokenCreator'
```

Repeat separately for each teammate. This is a **service-account-level** grant: it does not give them Owner/Editor access to the whole project. It does permit them to obtain credentials with whatever permissions the target service account currently has, so keep that service account least-privileged and audit its roles if its responsibilities grow.

Check the access list:

```bash
gcloud iam service-accounts get-iam-policy \
  mapmory-local-dev@mapmory-development.iam.gserviceaccount.com \
  --project=mapmory-development --format=yaml
```

Revoke an individual developer's access when no longer needed:

```bash
gcloud iam service-accounts remove-iam-policy-binding \
  mapmory-local-dev@mapmory-development.iam.gserviceaccount.com \
  --project=mapmory-development \
  --member='user:TEAMMATE_GOOGLE_EMAIL' \
  --role='roles/iam.serviceAccountTokenCreator'
```

**Important distinction:** If a developer needs to change Firebase Auth settings or view/manage users in Firebase Console, grant a **separate appropriately scoped project role**. Token Creator alone does not do that. Consider appointing a second trusted project administrator before handover; a single administrator is a team dependency.

## References

- [Firebase Admin SDK setup](https://firebase.google.com/docs/admin/setup)
- [Verify Firebase ID tokens](https://firebase.google.com/docs/auth/admin/verify-id-tokens)
- [Google Cloud service-account impersonation](https://cloud.google.com/docs/authentication/use-service-account-impersonation)
- [Firebase IAM roles](https://firebase.google.com/docs/projects/iam/roles)
