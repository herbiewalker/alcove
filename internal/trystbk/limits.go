package trystbk

// limits holds the size bounds actually enforced. They default to the exported
// constants; tests lower them so the hostile-file paths can be exercised with small
// inputs instead of gigabytes.
type limits struct {
	body              int64
	dataJSON          int64
	totalUncompressed uint64
	smallEntry        int64
	mediaEntry        uint64
	zipEntries        int
	rows              int
}

var lim = limits{
	body:              MaxBackupBytes,
	dataJSON:          MaxDataJSONBytes,
	totalUncompressed: MaxTotalUncompressedBytes,
	smallEntry:        MaxSmallEntryBytes,
	mediaEntry:        MaxMediaEntryBytes,
	zipEntries:        MaxZipEntries,
	rows:              MaxRows,
}
