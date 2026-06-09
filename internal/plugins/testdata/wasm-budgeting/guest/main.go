// The Go/WASM analogue of the budgeting test plugins: tag transactions whose
// description contains the configured keyword. Magic descriptions exercise
// failure modes the runtime must survive: "panic!" (recovered by the SDK),
// "exit!" (kills the module instance), "spin!" (runs until the hook deadline
// interrupts it).
package main

import (
	"fmt"
	"os"
	"strings"

	kasas "github.com/paulmeier/kasas/pluginsdk/kasas"
)

func init() {
	kasas.OnTransactionCreate(classify)
	kasas.OnTransactionUpdate(classify)
	kasas.OnSyncComplete(func(s *kasas.SyncSummary) error {
		kasas.Log("info", "sync complete", map[string]any{"new": s.NewTransactions})
		return nil
	})
	kasas.OnUninstall(func() error {
		kasas.Log("info", "uninstalling", nil)
		return nil
	})
	kasas.OnPageRender(renderPage)
	kasas.OnPageAction(func(req *kasas.PageRequest) (*kasas.Page, error) {
		if req.Action == "set-keyword" {
			if _, err := kasas.SetConfig(map[string]any{"keyword": req.Params["keyword"]}); err != nil {
				return nil, err
			}
		}
		return renderPage(req)
	})
}

func classify(t *kasas.Transaction) error {
	switch {
	case strings.Contains(t.Description, "panic!"):
		panic("guest panic requested")
	case strings.Contains(t.Description, "exit!"):
		os.Exit(3)
	case strings.Contains(t.Description, "spin!"):
		for { //nolint:staticcheck // deliberate runaway loop for the timeout test
		}
	}
	kw := strings.ToLower(kasas.ConfigString("keyword"))
	if kw != "" && strings.Contains(strings.ToLower(t.Description), kw) {
		if err := kasas.ApplyLabels(t.ID, map[string]string{"category": "food"}); err != nil {
			return err
		}
		if err := kasas.SetExtension(t.ID, "budgeting.flagged", true); err != nil {
			return err
		}
		kasas.Log("info", "tagged transaction", map[string]any{"id": t.ID})
	}
	return nil
}

func renderPage(_ *kasas.PageRequest) (*kasas.Page, error) {
	count := "n/a" // search needs transactions:read; render the page regardless
	if tagged, err := kasas.Search("label:category=food", 100); err == nil {
		count = fmt.Sprintf("%d", len(tagged))
	}
	return &kasas.Page{
		Title: "WASM Budgeting",
		Blocks: []kasas.Block{
			kasas.Stat("Tagged", count, "transactions labeled food"),
			kasas.Form("set-keyword", "Save",
				kasas.Field{Name: "keyword", Label: "Keyword", Kind: "text", Value: kasas.ConfigString("keyword")}),
		},
	}, nil
}

func main() {} // required by -buildmode=c-shared, never runs
