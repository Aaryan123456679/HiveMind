# Refund requests log

Invoice 4390 (Acme Retail Co., February) was refunded $249.00 after a
duplicate Stripe webhook delivery double-charged the account. Priya Nair
(finance) approved the refund; Marcus Webb (account manager) notified the
customer.

Separately, invoice 4602 (Bramble Foods) was refunded $89.50 for a
cancelled add-on seat that billing failed to remove before the renewal
cycle ran.

No pattern across customers yet beyond the Stripe webhook retry issue,
which engineering has been asked to fix at the source.
