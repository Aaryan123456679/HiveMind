# Provenance: `enron_real_sample/`

Issue #19 subtask 3.5.2. Six genuine, real Enron corpus messages, sourced to close the
forwarded finding `hivemind-issue19-3.5.2-need-real-enron-sample` (`.cdr/memory/pending.md`):
`data/fixtures/enron_sample/` (task-3.5.1) is hand-authored/invented content and was
explicitly flagged as insufficient for this subtask's "real dataset sample" requirement.

## Source

- Archive: `enron_mail_20150507.tar.gz`, the official CMU/FERC-hosted release,
  `https://www.cs.cmu.edu/~enron/enron_mail_20150507.tar.gz` (443,254,787 bytes,
  `Last-Modified: Thu, 07 May 2015`).
- License/status: the Enron email corpus is a matter of public record released via
  FERC's investigation into Enron and has been redistributed for research use by CMU
  since 2015 without restriction; this is the same widely-used academic corpus already
  referenced (as a format model, not a content source) by
  `agents/ingestion/testdata/enron_sample_*.txt` (issue #17) and by
  `data/load_enron.py`'s own module docstring.
- Extraction method: this repository does **not** download or commit the full ~423MB
  archive. Instead, the archive's gzip stream was opened directly via
  `urllib.request.urlopen` and read incrementally with Python's `tarfile` in streaming
  mode (`tarfile.open(fileobj=..., mode="r|gz")`), scanning tar members in archive order
  and stopping (closing the connection) as soon as 6 suitable messages were found --
  only ~1,733 of the archive's ~500k members were ever read off the wire. No intermediate
  file larger than one extracted message (under 2KB each) was ever written to disk.
- Selection criteria (applied during the single streaming scan above, not hand-picked
  by content): regular files under `maildir/blair-l/sent_items/` (an arbitrary starting
  mailbox/folder -- the first one the streaming scan reached), containing a `Message-ID`
  header, `From`/`To` headers, and between 400-3000 bytes (a plain, single/short-thread
  business message, not an empty stub or a huge multi-quote chain) -- the first 6 members
  meeting all three conditions were kept, in the order encountered.

## Files

`enron_real_001.txt` through `enron_real_006.txt`, verbatim (byte-for-byte) copies of six
such extracted messages, renamed sequentially (original archive filenames were
extensionless numeric maildir message IDs, e.g. `maildir/blair-l/sent_items/318.`) --
message content, all headers, and byte content are otherwise completely unmodified from
what the CMU archive itself served.

## Content note

All six messages are from Enron employee Lynn Blair's `sent_items` maildir folder --
ordinary internal business correspondence (scheduling, tariff-sheet review, gas-logistics
operational reporting). No selection was made to exclude or include any message based on
sensitivity; these are simply the first six messages the streaming scan's byte-size/header
filter matched in this mailbox.
