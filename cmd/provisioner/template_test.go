package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/s-urbaniak/dbaas/internal/provisioner"
)

func TestIndexTemplateRendersHeadlampAndDeleteActions(t *testing.T) {
	t.Parallel()

	data := pageData{
		HeadlampBaseURL: "http://127.0.0.1:4466",
		Workspaces: []provisioner.WorkspaceInfo{
			{
				Name:        "tenant-a",
				Status:      "Ready",
				StatusClass: "success",
			},
		},
	}

	var buf bytes.Buffer
	if err := indexTmpl.Execute(&buf, data); err != nil {
		t.Fatalf("indexTmpl.Execute() error = %v", err)
	}

	rendered := buf.String()
	if !strings.Contains(rendered, `href="http://127.0.0.1:4466/c/tenant-a"`) {
		t.Fatalf("rendered template missing Headlamp link: %s", rendered)
	}
	if !strings.Contains(rendered, ">Delete</button>") {
		t.Fatalf("rendered template missing Delete button: %s", rendered)
	}
}
