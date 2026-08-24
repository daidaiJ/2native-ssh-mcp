package tools

import "github.com/mark3labs/mcp-go/mcp"

func boolPtr(v bool) *bool { return &v }

func readOnlyAnnotation(title string) mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		Title:        title,
		ReadOnlyHint: boolPtr(true),
		OpenWorldHint: boolPtr(true),
	})
}

func mutatingAnnotation(title string, destructive bool) mcp.ToolOption {
	ann := mcp.ToolAnnotation{
		Title:         title,
		ReadOnlyHint:  boolPtr(false),
		OpenWorldHint: boolPtr(true),
	}
	if destructive {
		ann.DestructiveHint = boolPtr(true)
	}
	return mcp.WithToolAnnotation(ann)
}
