package config

import (
	"strings"
	"testing"
)

// The billing portal's export has changed columns over the years, so
// the importer matches by header name and falls back to content. Each
// case below is a shape a real export has taken.
func TestParseCSVAcceptsPortalExports(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantNames []string
		wantVEIDs []string
	}{
		{
			name: "veid and api_key headers",
			body: "VEID,API_KEY\n" +
				"1347645,private_aaaaaaaaaaaaaaaaaaaaaa\n" +
				"1347646,private_bbbbbbbbbbbbbbbbbbbbbb\n",
			wantNames: []string{"vps-1347645", "vps-1347646"},
			wantVEIDs: []string{"1347645", "1347646"},
		},
		{
			name: "hostname column becomes the name",
			body: "hostname,VEID,API KEY\n" +
				"tokyo.example.com,1347645,private_aaaaaaaaaaaaaaaaaaaaaa\n" +
				"osaka.example.com,1347646,private_bbbbbbbbbbbbbbbbbbbbbb\n",
			wantNames: []string{"tokyo.example.com", "osaka.example.com"},
			wantVEIDs: []string{"1347645", "1347646"},
		},
		{
			name: "spaced and cased headers",
			body: "  Vps ID , Api-Key , Service  \n" +
				"1347645,private_aaaaaaaaaaaaaaaaaaaaaa,web1\n",
			wantNames: []string{"web1"},
			wantVEIDs: []string{"1347645"},
		},
		{
			name: "no header, detected by content",
			body: "1347645,private_aaaaaaaaaaaaaaaaaaaaaa\n" +
				"1347646,private_bbbbbbbbbbbbbbbbbbbbbb\n",
			wantNames: []string{"vps-1347645", "vps-1347646"},
			wantVEIDs: []string{"1347645", "1347646"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCSV(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("ParseCSV: %v", err)
			}
			if len(got) != len(tc.wantNames) {
				t.Fatalf("imported %d rows, want %d: %+v", len(got), len(tc.wantNames), got)
			}
			for i, want := range tc.wantNames {
				if got[i].Name != want {
					t.Errorf("row %d name = %q, want %q", i, got[i].Name, want)
				}
				if got[i].Server.VEID != tc.wantVEIDs[i] {
					t.Errorf("row %d veid = %q, want %q", i, got[i].Server.VEID, tc.wantVEIDs[i])
				}
				if got[i].Server.APIKey == "" {
					t.Errorf("row %d has no key", i)
				}
			}
		})
	}
}

// Hostnames repeat across a fleet; the importer must not drop rows or
// produce colliding names.
func TestParseCSVDeduplicatesNames(t *testing.T) {
	body := "hostname,VEID,API_KEY\n" +
		"box,1,private_aaaaaaaaaaaaaaaaaaaaaa\n" +
		"box,2,private_bbbbbbbbbbbbbbbbbbbbbb\n" +
		"box,3,private_cccccccccccccccccccccc\n"

	got, err := ParseCSV(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("imported %d rows, want 3", len(got))
	}
	want := []string{"box", "box-2", "box-3"}
	seen := map[string]bool{}
	for i, im := range got {
		if im.Name != want[i] {
			t.Errorf("row %d name = %q, want %q", i, im.Name, want[i])
		}
		if seen[im.Name] {
			t.Errorf("duplicate name %q", im.Name)
		}
		seen[im.Name] = true
	}
}

func TestParseCSVSkipsUnusableRows(t *testing.T) {
	body := "VEID,API_KEY\n" +
		"1347645,private_aaaaaaaaaaaaaaaaaaaaaa\n" +
		",private_bbbbbbbbbbbbbbbbbbbbbb\n" + // no veid
		"1347647,\n" + // no key
		"not-a-number,private_cccccccccccccccccccccc\n" + // veid must be numeric
		"1347648,private_dddddddddddddddddddddd\n"

	got, err := ParseCSV(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("imported %d rows, want the 2 usable ones: %+v", len(got), got)
	}
}

func TestParseCSVSanitizesNames(t *testing.T) {
	body := "hostname,VEID,API_KEY\n" +
		"Tokyo Web #1!,1,private_aaaaaaaaaaaaaaaaaaaaaa\n" +
		"---,2,private_bbbbbbbbbbbbbbbbbbbbbb\n" // sanitizes to nothing

	got, err := ParseCSV(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "tokyo-web-1" {
		t.Errorf("name = %q, want it lowercased and punctuation-stripped", got[0].Name)
	}
	if got[1].Name != "vps-2" {
		t.Errorf("unusable name fell back to %q, want vps-2", got[1].Name)
	}
	for _, im := range got {
		if err := ValidateName(im.Name); err != nil {
			t.Errorf("imported name %q is not usable as an argument: %v", im.Name, err)
		}
	}
}

func TestParseCSVErrors(t *testing.T) {
	cases := map[string]string{
		"empty file":       "",
		"headers only":     "VEID,API_KEY\n",
		"nothing usable":   "colour,size\nred,large\n",
		"unbalanced quote": "VEID,API_KEY\n1,\"unterminated\n",
	}
	for name, body := range cases {
		if _, err := ParseCSV(strings.NewReader(body)); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
}

// A rejected file must say what shape it wanted, or the user has to
// guess at the portal's export format.
func TestParseCSVErrorIsActionable(t *testing.T) {
	_, err := ParseCSV(strings.NewReader("colour,size\nred,large\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"veid", "api_key", "private_"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
