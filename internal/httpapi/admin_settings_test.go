package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/pluginapi"
)

// fakeConfigurable is a settings-only plugin used to exercise the admin
// settings form and its bool field.
type fakeConfigurable struct {
	configured map[string]string
}

func (*fakeConfigurable) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: "fakecfg", APIVersion: pluginapi.APIVersion}
}
func (*fakeConfigurable) Match(*url.URL, pluginapi.Capability) bool { return false }
func (*fakeConfigurable) Discover(context.Context, string, pluginapi.Host) ([]pluginapi.Candidate, error) {
	return nil, pluginapi.ErrUnsupportedCapability
}
func (*fakeConfigurable) Fetch(context.Context, pluginapi.FetchRequest, pluginapi.Host) (pluginapi.Result, error) {
	return pluginapi.Result{}, pluginapi.ErrUnsupportedCapability
}
func (*fakeConfigurable) Settings() []pluginapi.SettingField {
	return []pluginapi.SettingField{
		{Name: "on_flag", Label: "on flag", Kind: "bool"},
		{Name: "base_url", Label: "base", Kind: "url"},
	}
}
func (f *fakeConfigurable) Configure(v map[string]string) { f.configured = v }

// TestAdminPluginBoolSetting asserts a bool setting saves as "1" when ticked and
// "0" when the box is absent, is pushed through Configure, and round-trips into
// the form's checked state.
func TestAdminPluginBoolSetting(t *testing.T) {
	s, _ := newTestServer(t)
	fp := &fakeConfigurable{}
	reg := plugin.NewRegistry()
	reg.RegisterNative(fp)
	s.plugins = reg

	save := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/plugins/fakecfg/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("name", "fakecfg")
		rr := httptest.NewRecorder()
		s.adminSavePluginSettings(rr, req)
		return rr
	}

	if rr := save(url.Values{"setting_on_flag": {"1"}, "setting_base_url": {"https://x"}}); rr.Code != http.StatusOK {
		t.Fatalf("save on: status %d", rr.Code)
	}
	vals := s.store.Settings.PluginSettings("fakecfg", []string{"on_flag", "base_url"})
	if vals["on_flag"] != "1" {
		t.Fatalf("stored on_flag = %q, want 1", vals["on_flag"])
	}
	if fp.configured["on_flag"] != "1" {
		t.Fatalf("Configure on_flag = %q", fp.configured["on_flag"])
	}
	if got := boolSettingChecked(t, s, "on_flag"); !got {
		t.Fatal("form should report the toggle checked")
	}

	// An unchecked box submits nothing; it must store "0".
	if rr := save(url.Values{"setting_base_url": {"https://x"}}); rr.Code != http.StatusOK {
		t.Fatalf("save off: status %d", rr.Code)
	}
	if vals := s.store.Settings.PluginSettings("fakecfg", []string{"on_flag"}); vals["on_flag"] != "0" {
		t.Fatalf("stored on_flag = %q, want 0", vals["on_flag"])
	}
	if got := boolSettingChecked(t, s, "on_flag"); got {
		t.Fatal("form should report the toggle unchecked")
	}
}

func boolSettingChecked(t *testing.T, s *Server, name string) bool {
	t.Helper()
	for _, row := range s.adminPluginSettings("fakecfg") {
		if row.Name == name {
			return row.Checked
		}
	}
	t.Fatalf("setting %q not found", name)
	return false
}
