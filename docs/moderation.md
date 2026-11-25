# Manual moderation of public shares

## Overview

MapMory allows anonymous visitors to report publicly shared photo collections through `POST /api/shares/{share_id}/report`.

Submitting a valid report immediately hides the affected collection from public discovery:

- `GET /api/public/map` no longer includes the reported share.
- `GET /api/public/shares/{share_id}` no longer returns the reported public collection.
- Existing token-based sharing links remain accessible.
- The underlying trip, photos, and share records are preserved.

This is a provisional moderation mechanism. There is currently no administrative moderation interface, notification system, or automated review process.

## Reviewing pending reports

An authorized maintainer can inspect unresolved reports using read-only access to the application database:

```sql
SELECT
    r.id,
    r.share_id,
    r.reason,
    r.created_at
FROM share_reports AS r
WHERE r.resolved_at IS NULL
ORDER BY r.created_at ASC;
```

Review the reported reason and the associated collection before deciding whether the report is valid.

Do not assume that a report is accurate merely because it was submitted. Reports are anonymous and may be mistaken or malicious.

## Resolving a report

If the collection is acceptable and should become publicly discoverable again, an authorized maintainer may mark the report as resolved:

```sql
UPDATE share_reports
SET resolved_at = now()
WHERE id = '<REPORT_UUID>'
  AND resolved_at IS NULL
RETURNING id, share_id, resolved_at;
```

Use the report's actual UUID. Verify the returned row before proceeding.

A share remains hidden while **any unresolved report** exists for that share. Review all unresolved reports concerning the same collection before restoring visibility.

After resolution, verify that the collection is accessible through the public collection endpoint and appears in public discovery, provided the share remains active.

## Confirmed harmful content

Do not mark a valid report as resolved merely to restore public visibility.

Escalate the report to the project maintainers. The authorized owner may be asked to remove or unpublish the content.

The current implementation does not provide an administrator-only removal endpoint or a permanent moderation decision. If urgent action is required, maintainers should agree on an appropriate administrative intervention and record what was done.

## Operational precautions

- Only authorized maintainers should access the database or modify moderation records.
- Prefer read-only database access for inspection and narrowly scoped updates for resolution.
- Never run unrestricted `UPDATE` or `DELETE` statements against `share_reports`.
- Record moderation decisions, including the report UUID, decision, date, and responsible maintainer, in the team's private issue tracker or operational log.
- Avoid copying sensitive or objectionable images into public issues or chat messages.
- Do not expose database credentials in documentation or logs.
- Check unresolved reports regularly while public sharing is enabled.

## Known limitations

- A single accepted anonymous report can temporarily hide an otherwise legitimate collection.
- Rate limiting reduces report volume but cannot prevent false reports.
- Reports do not revoke existing token-based sharing links.
- No notification is generated when a report is submitted.
- No administrator interface or appeal mechanism is available.
- Moderation decisions require manual intervention.

Future improvements may include a moderation queue, notifications, stronger abuse protection, explicit review decisions, and administrative content-removal tools.
