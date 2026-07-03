package catalog

import (
	"reflect"
	"testing"
)

func TestRecordEncodeDecode(t *testing.T) {
	tests := []struct {
		name string
		rec  CatalogRecord
	}{
		{
			name: "zero value",
			rec:  CatalogRecord{},
		},
		{
			name: "populated with several redirect targets",
			rec: CatalogRecord{
				FileID:            42,
				PathHash:          0xDEADBEEFCAFEF00D,
				CurrentVersion:    7,
				SizeBytes:         123456,
				Status:            StatusSplit,
				RedirectTargetIDs: []uint64{43, 44, 45},
				ParentTopicID:     10,
				LastModified:      1_700_000_000_123_456_789,
			},
		},
		{
			name: "max redirect targets",
			rec: CatalogRecord{
				FileID:            1,
				PathHash:          2,
				CurrentVersion:    3,
				SizeBytes:         4,
				Status:            StatusRedirect,
				RedirectTargetIDs: []uint64{10, 11, 12, 13, 14, 15, 16, 17},
				ParentTopicID:     5,
				LastModified:      -1, // exercise negative/large-unsigned round trip
			},
		},
		{
			name: "active status with zero redirect targets but other fields set",
			rec: CatalogRecord{
				FileID:         99,
				PathHash:       100,
				CurrentVersion: 1,
				SizeBytes:      0,
				Status:         StatusActive,
				ParentTopicID:  0,
				LastModified:   1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.rec.Encode()
			if len(encoded) != RecordEncodedSize {
				t.Fatalf("Encode() returned %d bytes, want fixed size %d", len(encoded), RecordEncodedSize)
			}

			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() returned unexpected error: %v", err)
			}

			// Decode's convention: an empty RedirectTargetIDs decodes to nil, not an
			// empty non-nil slice. Normalize the expected value the same way so the
			// round-trip comparison reflects "no data loss", not slice-identity noise.
			want := tt.rec
			if len(want.RedirectTargetIDs) == 0 {
				want.RedirectTargetIDs = nil
			}

			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("round-trip mismatch:\n  got:  %+v\n  want: %+v", decoded, want)
			}
		})
	}
}

func TestDecodeRejectsWrongLength(t *testing.T) {
	_, err := Decode(make([]byte, RecordEncodedSize-1))
	if err == nil {
		t.Fatal("Decode() with short buffer: expected error, got nil")
	}

	_, err = Decode(make([]byte, RecordEncodedSize+1))
	if err == nil {
		t.Fatal("Decode() with long buffer: expected error, got nil")
	}
}

func TestDecodeRejectsOutOfRangeRedirectCount(t *testing.T) {
	buf := CatalogRecord{}.Encode()
	buf[offRedirectCount] = byte(MaxRedirectTargets + 1)

	_, err := Decode(buf)
	if err == nil {
		t.Fatal("Decode() with out-of-range redirect count: expected error, got nil")
	}
}
