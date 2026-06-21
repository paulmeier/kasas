package plugins

// sourceTypePrefix namespaces a plugin ingestion source's type and provenance
// stamp (ADR 0005), keeping it distinct from every built-in source's type.
const sourceTypePrefix = "plugin:"

// SourceType returns the ingestion-source type / provenance stamp for a plugin
// source: "plugin:<name>". It is one identity — the engine key, the Sources-page
// type, and the transactions.source value for rows the plugin produced — derived
// from the plugin name, which the plugin cannot forge.
func SourceType(name string) string { return sourceTypePrefix + name }
