package trystbk

// SchemaSupport says how much Alcove trusts a Tryst database schema version.
type SchemaSupport int

const (
	// SchemaKnown: the version is one Alcove has seen documented (<= LatestKnownSchema).
	// Reading is generic, so it works either way; "known" says the metrics registry and
	// the editor have been written against this layout.
	SchemaKnown SchemaSupport = iota
	// SchemaNewer: the backup comes from a newer Tryst than Alcove knows. It is held and
	// can be read, but editing and export must stay disabled until the version is added
	// to the compatibility table and proven on an emulator (F10, THREAT_MODEL R6).
	SchemaNewer
	// SchemaInvalid: a schema version below 1 cannot be right.
	SchemaInvalid
)

// LatestKnownSchema is the newest Tryst database schema Alcove has documented. v19 is
// read from Tryst's app/schemas/.../19.json. Bump it only together with the metrics
// registry, the editor and an emulator round trip (the recurring compatibility check).
const LatestKnownSchema = 19

// SchemaStatus classifies a Tryst schema version.
func SchemaStatus(v int) SchemaSupport {
	switch {
	case v < 1:
		return SchemaInvalid
	case v > LatestKnownSchema:
		return SchemaNewer
	default:
		return SchemaKnown
	}
}

// RestoreTables is the order Tryst inserts tables on restore (children after parents).
// It is the table list in Tryst's BackupManager; `recent_searches` is deliberately not
// part of a backup, and a restore leaves that local table alone (D-15).
var RestoreTables = []string{
	"partners", "profile", "locations", "tags", "positions", "acts", "kinks", "toys",
	"occasions", "ejaculation_locations", "orientations",
	"encounters", "media", "person_photo",
	"encounter_partner", "encounter_position", "encounter_tag",
}
