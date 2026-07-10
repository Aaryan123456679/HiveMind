# Invoice 4521 dispute

Customer Acme Retail Co. flagged invoice 4521 as double-billed for the March
subscription tier. Finance (Priya Nair) confirmed the duplicate charge was
caused by a retry in the Stripe webhook handler after a timeout.

A refund of $249.00 was issued via Stripe on 2026-03-18. The customer's
account manager, Marcus Webb, followed up to confirm receipt.

Related: this is the second duplicate-charge report this quarter involving
the Stripe webhook retry path; see the refund-requests topic for the first
occurrence (invoice 4390, February).
