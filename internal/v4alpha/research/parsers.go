// Backend response parsers. Each parser is a pure function that
// turns a backend's raw body (the bytes HTTPClient returned) into
// a slice of research.Item values the executor can merge.
//
// The parsers are pure: no I/O, no time, no randomness. They are
// tested with snapshot fixtures (recorded responses from the 2026-
// 09-27 probe) so the snapshot tests do not hit the network. The
// parsers do not perform injection scan or redaction: the executor
// runs research_gate.Check on the assembled Result before returning
// to the operator, and the gate is the single chokepoint for both
// INV-13 (redact-before-log) and Tier-1 control 7 (injection scan).
package research

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseFunc is the signature every backend parser satisfies.
// Inputs are the raw body and the canonical target id the executor
// resolved. The returned Result has Intent + Sources set; the
// executor fills FetchedAt and (in the merge) VibeCase.
//
// Returning an empty Items slice is NOT an error: the backend was
// reachable and answered, just with no hits. The executor
// distinguishes "empty" from "unreachable" by whether ParseFunc
// returned an error.
type ParseFunc func(body []byte, target string) (Result, error)

// parsers is the registry the executor consults by name. Each
// entry must have a matching fixture under research_fixtures/
// (or the snapshot test for that backend will skip with a clear
// message).
var parsers = map[string]ParseFunc{
	"osv":       parseOSV,
	"nvd":       parseNVD,
	"npm":       parseNPM,
	"crates":    parseCrates,
	"github":    parseGitHub,
	"openalex":  parseOpenAlex,
	"crossref":  parseCrossref,
	"arxiv":     parseArXiv,
	"rdap":      parseRDAP,
	"doh":       parseDoH,
	"ip-api":    parseIPAPI,
	"ripe":      parseRIPE,
	"crt":       parseCRT,
	"hibp":      parseHIBP,
	"nominatim": parseNominatim,
	"hn":        parseHN,
	"gdelt":     parseGDELT,
	"ddg":       parseDDG,
}

// ParserFor returns the registered parser for name, or nil if no
// parser is registered. The executor checks nil and fails the
// backend at boot (a typo in a parser name is a developer bug,
// not a runtime degradation).
func ParserFor(name string) ParseFunc {
	return parsers[name]
}

// RegisterParser installs a parser under name. Useful for tests
// that want to swap a parser; production backends register via
// the parsers map literal above.
func RegisterParser(name string, fn ParseFunc) {
	parsers[name] = fn
}

// ---------- CVE ----------

// parseOSV unwraps the api.osv.dev single-CVE response. The
// summary is the title; details (Markdown) is the snippet;
// references[].url is the canonical URL.
func parseOSV(body []byte, target string) (Result, error) {
	var v struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Details  string `json:"details"`
		Severity []struct {
			Type  string `json:"type"`
			Score string `json:"score"`
		} `json:"severity"`
		References []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"references"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("osv: %w", err)
	}
	title := v.ID
	if v.Summary != "" {
		title = v.ID + " — " + truncate(v.Summary, 120)
	}
	url := "https://osv.dev/vulnerability/" + v.ID
	if len(v.References) > 0 {
		url = v.References[0].URL
	}
	snippet := truncate(v.Details, 280)
	return Result{
		Intent:    IntentCVE,
		Query:     target,
		Items:     []Item{{Title: title, URL: url, Snippet: snippet, Confidence: 0.95}},
		Sources:   []string{"osv.dev"},
		FetchedAt: time.Time{}, // executor sets this
	}, nil
}

// parseNVD unwraps the NVD CVE 2.0 response. Each vulnerability
// becomes one Item; severity is the CVSSv3 base score when present.
func parseNVD(body []byte, target string) (Result, error) {
	var v struct {
		Vulnerabilities []struct {
			CVE struct {
				ID           string `json:"id"`
				Descriptions []struct {
					Lang  string `json:"lang"`
					Value string `json:"value"`
				} `json:"descriptions"`
				Metrics struct {
					CVSSMetricV31 []struct {
						CVSSData struct {
							BaseScore    float64 `json:"baseScore"`
							BaseSeverity string   `json:"baseSeverity"`
						} `json:"cvssData"`
					} `json:"cvssMetricV31"`
				} `json:"metrics"`
			} `json:"cve"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("nvd: %w", err)
	}
	var items []Item
	for _, w := range v.Vulnerabilities {
		desc := ""
		for _, d := range w.CVE.Descriptions {
			if d.Lang == "en" {
				desc = d.Value
				break
			}
		}
		if desc == "" && len(w.CVE.Descriptions) > 0 {
			desc = w.CVE.Descriptions[0].Value
		}
		title := w.CVE.ID
		if len(w.CVE.Metrics.CVSSMetricV31) > 0 {
			title = w.CVE.ID + " (CVSS " +
				strconv.FormatFloat(w.CVE.Metrics.CVSSMetricV31[0].CVSSData.BaseScore, 'f', 1, 64) +
				" " + w.CVE.Metrics.CVSSMetricV31[0].CVSSData.BaseSeverity + ")"
		}
		items = append(items, Item{
			Title:      title,
			URL:        "https://nvd.nist.gov/vuln/detail/" + w.CVE.ID,
			Snippet:    truncate(desc, 280),
			Confidence: 0.95,
		})
	}
	return Result{
		Intent:    IntentCVE,
		Query:     target,
		Items:     items,
		Sources:   []string{"nvd.nist.gov"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Code ----------

// parseNPM unwraps the npm registry /{name}/latest response.
// Title is name@version; URL is the registry page.
func parseNPM(body []byte, target string) (Result, error) {
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("npm: %w", err)
	}
	name, _ := v["name"].(string)
	version, _ := v["version"].(string)
	if name == "" {
		name = target
	}
	description, _ := v["description"].(string)
	deprecated, _ := v["deprecated"].(string)
	snippet := description
	if deprecated != "" {
		snippet = "DEPRECATED: " + deprecated + " — " + description
	}
	return Result{
		Intent:  IntentCode,
		Query:   target,
		Items:   []Item{{Title: name + "@" + version, URL: "https://www.npmjs.com/package/" + name, Snippet: truncate(snippet, 280), Confidence: 0.95}},
		Sources: []string{"registry.npmjs.org"},
		FetchedAt: time.Time{},
	}, nil
}

// parseCrates unwraps the crates.io /api/v1/crates/{name} envelope.
// We return a synthetic "name@version" Item per recent version
// (capped at 5) so a multi-version crate produces a corroboration
// chain.
func parseCrates(body []byte, target string) (Result, error) {
	var v struct {
		Crate struct {
			Name        string `json:"name"`
			MaxVersion  string `json:"max_version"`
			Description string `json:"description"`
			UpdatedAt   string `json:"updated_at"`
		} `json:"crate"`
		Versions []struct {
			Num string `json:"num"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("crates: %w", err)
	}
	name := v.Crate.Name
	if name == "" {
		name = target
	}
	items := []Item{{
		Title:      name + "@" + v.Crate.MaxVersion,
		URL:        "https://crates.io/crates/" + name,
		Snippet:    truncate(v.Crate.Description, 280),
		Confidence: 0.95,
	}}
	for i, ver := range v.Versions {
		if i >= 4 {
			break
		}
		items = append(items, Item{
			Title:      name + "@" + ver.Num,
			URL:        "https://crates.io/crates/" + name + "/" + ver.Num,
			Snippet:    "(recent version)",
			Confidence: 0.6,
		})
	}
	return Result{
		Intent:    IntentCode,
		Query:     target,
		Items:     items,
		Sources:   []string{"crates.io"},
		FetchedAt: time.Time{},
	}, nil
}

// parseGitHub unwraps the GitHub repository search response.
func parseGitHub(body []byte, target string) (Result, error) {
	var v struct {
		TotalCount int `json:"total_count"`
		Items      []struct {
			FullName        string `json:"full_name"`
			HTMLURL         string `json:"html_url"`
			Description     string `json:"description"`
			StargazersCount int    `json:"stargazers_count"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("github: %w", err)
	}
	var items []Item
	for i, w := range v.Items {
		if i >= 5 {
			break
		}
		title := w.FullName
		if w.StargazersCount > 0 {
			title = w.FullName + " (" + strconv.Itoa(w.StargazersCount) + "★)"
		}
		items = append(items, Item{
			Title:      title,
			URL:        w.HTMLURL,
			Snippet:    truncate(w.Description, 280),
			Confidence: 0.85,
		})
	}
	return Result{
		Intent:    IntentCode,
		Query:     target,
		Items:     items,
		Sources:   []string{"api.github.com"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Academic ----------

// parseOpenAlex unwraps the OpenAlex /works search response.
func parseOpenAlex(body []byte, target string) (Result, error) {
	var v struct {
		Results []struct {
			Title    string `json:"title"`
			DOI      string `json:"doi"`
			URL      string `json:"url"`
			Abstract string `json:"abstract_inverted_index"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("openalex: %w", err)
	}
	var items []Item
	for i, w := range v.Results {
		if i >= 5 {
			break
		}
		url := w.URL
		if url == "" && w.DOI != "" {
			url = "https://doi.org/" + w.DOI
		}
		items = append(items, Item{
			Title:      w.Title,
			URL:        url,
			Snippet:    "(OpenAlex record)",
			Confidence: 0.85,
		})
	}
	return Result{
		Intent:    IntentAcademic,
		Query:     target,
		Items:     items,
		Sources:   []string{"api.openalex.org"},
		FetchedAt: time.Time{},
	}, nil
}

// parseCrossref unwraps the Crossref /works search response.
func parseCrossref(body []byte, target string) (Result, error) {
	var v struct {
		Message struct {
			Items []struct {
				Title    []string `json:"title"`
				DOI      string   `json:"DOI"`
				URL      string   `json:"URL"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("crossref: %w", err)
	}
	var items []Item
	for i, w := range v.Message.Items {
		if i >= 5 {
			break
		}
		title := ""
		if len(w.Title) > 0 {
			title = w.Title[0]
		}
		items = append(items, Item{
			Title:      title,
			URL:        w.URL,
			Snippet:    "(Crossref record)",
			Confidence: 0.85,
		})
	}
	return Result{
		Intent:    IntentAcademic,
		Query:     target,
		Items:     items,
		Sources:   []string{"api.crossref.org"},
		FetchedAt: time.Time{},
	}, nil
}

// arxivFeed is the partial arXiv Atom shape we care about.
type arxivFeed struct {
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Summary string   `xml:"summary"`
	Authors []struct {
		Name string `xml:"name"`
	} `xml:"author"`
}

// parseArXiv unwraps the arXiv Atom feed. The executor calls the
// URL with https (not http) so the redirect dance that broke the
// 2026-09-27 probe is not needed.
func parseArXiv(body []byte, target string) (Result, error) {
	var f arxivFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		return Result{}, fmt.Errorf("arxiv: %w", err)
	}
	var items []Item
	for i, e := range f.Entries {
		if i >= 5 {
			break
		}
		title := strings.TrimSpace(e.Title)
		url := strings.TrimSpace(e.ID)
		items = append(items, Item{
			Title:      title,
			URL:        url,
			Snippet:    truncate(e.Summary, 280),
			Confidence: 0.8,
		})
	}
	return Result{
		Intent:    IntentAcademic,
		Query:     target,
		Items:     items,
		Sources:   []string{"export.arxiv.org"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Domain ----------

// parseRDAP unwraps the RDAP domain response.
func parseRDAP(body []byte, target string) (Result, error) {
	var v struct {
		LDHName    string `json:"ldhName"`
		Handle     string `json:"handle"`
		Status    []string `json:"status"`
		Events    []struct {
			EventAction string `json:"eventAction"`
			EventDate   string `json:"eventDate"`
		} `json:"events"`
		Entities []struct {
			Roles     []string `json:"roles"`
			VCardArray [][]any `json:"vcardArray"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("rdap: %w", err)
	}
	snippet := "status=" + strings.Join(v.Status, ",")
	if len(v.Events) > 0 {
		snippet += "; registered=" + v.Events[0].EventDate
	}
	return Result{
		Intent:  IntentDomain,
		Query:   target,
		Items:   []Item{{Title: v.LDHName, URL: "https://rdap.org/domain/" + v.LDHName, Snippet: snippet, Confidence: 0.9}},
		Sources: []string{"rdap.org"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- DNS ----------

// parseDoH unwraps a DoH JSON response (RFC 8427). The Answer
// section is flattened to one Item per record; private IPs are
// filtered at the response side by security.FilterPrivateIPs.
func parseDoH(body []byte, target string) (Result, error) {
	var v struct {
		Status int
		Answer []struct {
			Name string `json:"name"`
			Type int    `json:"type"`
			TTL  int    `json:"TTL"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("doh: %w", err)
	}
	var items []Item
	for _, a := range v.Answer {
		items = append(items, Item{
			Title:      a.Name,
			URL:        "https://dns.google/resolve?name=" + url.QueryEscape(a.Name),
			Snippet:    "type=" + strconv.Itoa(a.Type) + " data=" + a.Data + " ttl=" + strconv.Itoa(a.TTL),
			Confidence: 0.9,
		})
	}
	return Result{
		Intent:    IntentDNS,
		Query:     target,
		Items:     items,
		Sources:   []string{"dns.google"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- IP ----------

// parseIPAPI unwraps the ip-api.com response.
func parseIPAPI(body []byte, target string) (Result, error) {
	var v struct {
		Status    string `json:"status"`
		Country   string `json:"country"`
		CountryCode string `json:"countryCode"`
		Region    string `json:"region"`
		City      string `json:"city"`
		Org       string `json:"org"`
		Query     string `json:"query"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("ip-api: %w", err)
	}
	if v.Status != "success" {
		return Result{}, fmt.Errorf("ip-api: %s", v.Status)
	}
	snippet := v.Country + " (" + v.CountryCode + ") " + v.Region + " " + v.City + " — " + v.Org
	return Result{
		Intent:  IntentIP,
		Query:   target,
		Items:   []Item{{Title: v.Query, URL: "https://ip-api.com/" + v.Query, Snippet: snippet, Confidence: 0.85}},
		Sources: []string{"ip-api.com"},
		FetchedAt: time.Time{},
	}, nil
}

// parseRIPE unwraps the RIPE abuse-contact response. The RIPE
// response is a redirect locator pointing to the authoritative
// server; we record that the query was made (RIPE is corroborant
// for IP-API T1).
func parseRIPE(body []byte, target string) (Result, error) {
	var v struct {
		Service struct {
			Name string `json:"name"`
		} `json:"service"`
		Link struct {
			Href string `json:"href"`
		} `json:"link"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("ripe: %w", err)
	}
	return Result{
		Intent:  IntentIP,
		Query:   target,
		Items:   []Item{{Title: target, URL: v.Link.Href, Snippet: "(RIPE abuse-contact locator)", Confidence: 0.7}},
		Sources: []string{"rest.db.ripe.net"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Cert ----------

// parseCRT unwraps the crt.sh JSON response. Each certificate
// is one Item. The byte cap (operator-controlled, 64 KB max for
// crt.sh) bounds the result.
func parseCRT(body []byte, target string) (Result, error) {
	var v []struct {
		ID           int64  `json:"id"`
		IssuerName   string `json:"issuer_name"`
		CommonName   string `json:"common_name"`
		NotBefore    string `json:"not_before"`
		NotAfter     string `json:"not_after"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("crt: %w", err)
	}
	var items []Item
	for i, c := range v {
		if i >= 5 {
			break
		}
		title := c.CommonName
		if title == "" {
			title = c.IssuerName
		}
		items = append(items, Item{
			Title:      title,
			URL:        "https://crt.sh/?id=" + strconv.FormatInt(c.ID, 10),
			Snippet:    "not_before=" + c.NotBefore + " not_after=" + c.NotAfter,
			Confidence: 0.85,
		})
	}
	return Result{
		Intent:    IntentCert,
		Query:     target,
		Items:     items,
		Sources:   []string{"crt.sh"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Email (HIBP k-anonymity) ----------

// parseHIBP unwraps the HIBP /range response. The body is a
// newline-separated list of "<sha1-suffix>:<count>". A count of
// 0 means the suffix has not been seen. We surface only suffixes
// with count > 0.
func parseHIBP(body []byte, target string) (Result, error) {
	var items []Item
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || count == 0 {
			continue
		}
		items = append(items, Item{
			Title:      target + ":" + strings.ToUpper(parts[0]),
			URL:        "https://haveibeenpwned.com/",
			Snippet:    "seen " + strconv.Itoa(count) + " times in breaches",
			Confidence: 0.9,
		})
		if len(items) >= 5 {
			break
		}
	}
	return Result{
		Intent:    IntentEmail,
		Query:     target,
		Items:     items,
		Sources:   []string{"api.pwnedpasswords.com"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Geo ----------

// parseNominatim unwraps the Nominatim search response.
func parseNominatim(body []byte, target string) (Result, error) {
	var v []struct {
		DisplayName string  `json:"display_name"`
		Lat         string  `json:"lat"`
		Lon         string  `json:"lon"`
		Type        string  `json:"type"`
		Importance  float64 `json:"importance"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("nominatim: %w", err)
	}
	var items []Item
	for i, p := range v {
		if i >= 5 {
			break
		}
		items = append(items, Item{
			Title:      p.DisplayName,
			URL:        "https://nominatim.openstreetmap.org/?q=" + url.QueryEscape(p.DisplayName),
			Snippet:    p.Lat + "," + p.Lon + " (" + p.Type + ")",
			Confidence: 0.7 + 0.3*p.Importance,
		})
	}
	return Result{
		Intent:    IntentGeo,
		Query:     target,
		Items:     items,
		Sources:   []string{"nominatim.openstreetmap.org"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- News ----------

// parseHN unwraps the HN Algolia search response.
func parseHN(body []byte, target string) (Result, error) {
	var v struct {
		Hits []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			StoryText   string `json:"story_text"`
			NumComments int    `json:"num_comments"`
			Points      int    `json:"points"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("hn: %w", err)
	}
	var items []Item
	for i, h := range v.Hits {
		if i >= 5 {
			break
		}
		title := h.Title
		if title == "" {
			title = h.URL
		}
		items = append(items, Item{
			Title:      title,
			URL:        h.URL,
			Snippet:    truncate(h.StoryText, 280),
			Confidence: 0.7,
		})
	}
	return Result{
		Intent:    IntentNews,
		Query:     target,
		Items:     items,
		Sources:   []string{"hn.algolia.com"},
		FetchedAt: time.Time{},
	}, nil
}

// parseGDELT unwraps the GDELT 2.0 doc response. We keep the
// raw article records.
func parseGDELT(body []byte, target string) (Result, error) {
	var v []struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Date        string `json:"date"`
		SocialImage string `json:"socialimage"`
		Language    string `json:"language"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Result{}, fmt.Errorf("gdelt: %w", err)
	}
	var items []Item
	for i, a := range v {
		if i >= 5 {
			break
		}
		items = append(items, Item{
			Title:      a.Title,
			URL:        a.URL,
			Snippet:    a.Date + " lang=" + a.Language,
			Confidence: 0.6,
		})
	}
	return Result{
		Intent:    IntentNews,
		Query:     target,
		Items:     items,
		Sources:   []string{"api.gdeltproject.org"},
		FetchedAt: time.Time{},
	}, nil
}

// ---------- Web (DDG HTML scrape) ----------

// regexDDGResult extracts the result title / URL from one DDG HTML
// result anchor. DDG renders results inside <a class="result__a"
// href="...">title</a>. We match a permissive subset.
var regexDDGResultAnchor = regexp.MustCompile(`<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
var regexDDGStripTags = regexp.MustCompile(`<[^>]+>`)

// parseDDG unwraps a DDG HTML search-results page. Each result
// anchor becomes one Item. The executor MUST redact the resulting
// snippet (HTML can carry payloads); the gate is responsible.
func parseDDG(body []byte, target string) (Result, error) {
	matches := regexDDGResultAnchor.FindAllSubmatch(body, -1)
	var items []Item
	for i, m := range matches {
		if i >= 5 {
			break
		}
		href := string(m[1])
		// DDG wraps target URLs in /l/?uddg=...; unwrap the actual
		// target. Failure to unwrap leaves the DDG link, which is
		// still a valid URL the gate can scan.
		if u, err := url.QueryUnescape(href); err == nil {
			href = u
		}
		title := regexDDGStripTags.ReplaceAllString(string(m[2]), "")
		items = append(items, Item{
			Title:      strings.TrimSpace(title),
			URL:        href,
			Snippet:    "(DuckDuckGo result)",
			Confidence: 0.6,
		})
	}
	return Result{
		Intent:    IntentWeb,
		Query:     target,
		Items:     items,
		Sources:   []string{"html.duckduckgo.com"},
		FetchedAt: time.Time{},
	}, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
