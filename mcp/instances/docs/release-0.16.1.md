# Instances 0.16.1

A native `delete` for a deployment ID this backend never recorded now writes a
deleted tombstone and returns `deleted` instead of HTTP 404. Apteva Server
discards a failed candidate by deleting it; when target inspection, validation
or the prepare response failed, the 404 left the candidate pending and every
redeploy was refused until the database was edited by hand.

A late or retried `prepare` for a tombstoned ID returns the deleted record
(or 409 when the request changed), so it can never create a service. The
tombstone checks only that the request addresses this Instances installation;
it touches nothing on the target, so an unreachable instance does not block
it. Other actions on unknown IDs still return 404.
