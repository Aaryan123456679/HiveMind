# On-call runbook: Stripe webhook retry storm

Symptom: duplicate `PutSegment`-equivalent billing writes traced back to the
Stripe webhook handler retrying a request that actually succeeded but timed
out on the response leg.

Mitigation: the webhook handler now checks an idempotency key
(`stripe_event_id`) against a dedupe table before applying a charge. Owner:
Marcus Webb (platform team). Deployed 2026-03-20.

Escalation path: page #platform-oncall; do not attempt a manual refund
without confirming with finance (Priya Nair) first, since Stripe's own
dashboard may already show the charge as reversed.
