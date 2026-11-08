package workspace

// StandardToolCatalog is the runtime host-tool catalog every member may use.
// Member tool configuration is retired: this is the single effective standard
// template, and the live permission mode, the task/role constraint, the run
// action ceiling and the conversation grants decide what may actually run.
//
// The order mirrors the historical member template so the existing UI labels and
// documentation stay accurate.
func StandardToolCatalog() []string {
	return []string{
		"list_files", "read_file", "view_image", "search_content",
		"web_search", "fetch_url", "read_source", "publish_artifact", "publish_file",
		"write_file", "patch_file", "run_test", "run_command",
	}
}

// StandardRoleTools projects the role capability onto the standard catalog.
// Publishing roles need publish_artifact; every other role needs nothing beyond
// the catalog. It never consults a legacy member array.
func StandardRoleTools(role string) []string {
	switch role {
	case "coordinator", "reviewer", "final_reviewer":
		return []string{"publish_artifact"}
	}
	return nil
}

// LegacyToolHistory reports whether a stored tool array differs from the standard
// catalog. Such values are audit data only; nothing in execution reads them.
func LegacyToolHistory(tools []string) bool {
	if len(tools) == 0 {
		return false
	}
	catalog := StandardToolCatalog()
	if len(tools) != len(catalog) {
		return true
	}
	for i, name := range catalog {
		if tools[i] != name {
			return true
		}
	}
	return false
}
