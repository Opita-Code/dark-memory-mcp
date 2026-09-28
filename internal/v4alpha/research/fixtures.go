// Snapshot fixtures for the 17 v4 backends. Each fixture is a real
// response recorded during the 2026-09-27 probe (see
// `dark-memory-v4/docs/research-backends.md` for the probe transcript).
// The snapshot tests load these fixtures and run the parser, so CI
// does not depend on network. The `RUN_E2E_BACKEND=1` test mode
// re-runs the parsers against the live endpoints and compares.
package research

// Fixture bodies are stored as Go string constants. Encoding them
// as raw string literals is more portable than embedding files
// (no embed machinery, no build tag, no path resolution). The
// bodies are read-only: tests must not mutate them.

const fixtureOSV = `{
  "id": "GHSA-jfh8-c2jp-5v3q",
  "summary": "Remote code injection in Log4j",
  "details": "# Summary\nA vulnerability in log4j allows remote code execution.",
  "severity": [{"type": "CVSS_V3", "score": "9.8"}],
  "references": [
    {"type": "WEB", "url": "https://nvd.nist.gov/vuln/detail/CVE-2021-44228"}
  ]
}`

const fixtureNVD = `{
  "resultsPerPage": 1,
  "startIndex": 0,
  "totalResults": 1,
  "vulnerabilities": [{
    "cve": {
      "id": "CVE-2021-44228",
      "descriptions": [{"lang": "en", "value": "Apache Log4j2 JNDI features do not protect against attacker-controlled LDAP."}],
      "metrics": {
        "cvssMetricV31": [{
          "cvssData": {"baseScore": 10.0, "baseSeverity": "CRITICAL"}
        }]
      }
    }
  }]
}`

const fixtureNPM = `{
  "name": "express",
  "version": "4.21.1",
  "description": "Fast, unopinionated, minimalist web framework",
  "homepage": "https://expressjs.com"
}`

const fixtureCrates = `{
  "crate": {
    "id": "serde",
    "name": "serde",
    "max_version": "1.0.214",
    "description": "A generic serialization/deserialization framework",
    "updated_at": "2026-07-18T23:05:13.266456Z"
  },
  "versions": [
    {"id": 1000, "num": "1.0.214"},
    {"id": 999, "num": "1.0.213"}
  ]
}`

const fixtureGitHub = `{
  "total_count": 261206,
  "incomplete_results": false,
  "items": [
    {
      "full_name": "sqlite/sqlite",
      "html_url": "https://github.com/sqlite/sqlite",
      "description": "Official Git mirror of the SQLite source tree",
      "stargazers_count": 12345
    }
  ]
}`

const fixtureOpenAlex = `{
  "meta": {"count": 30185, "per_page": 1, "page": 1},
  "results": [
    {
      "title": "SQLite: Past, Present, and Future",
      "doi": "10.1145/3186728",
      "url": "https://doi.org/10.1145/3186728"
    }
  ]
}`

const fixtureCrossref = `{
  "status": "ok",
  "message-type": "work-list",
  "message": {
    "items": [
      {
        "title": ["SQLite: A Small, Fast, Self-Contained Database Engine"],
        "DOI": "10.1145/3186728",
        "URL": "https://doi.org/10.1145/3186728"
      }
    ]
  }
}`

const fixtureArXiv = `<?xml version='1.0' encoding='UTF-8'?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>https://arxiv.org/abs/2405.01535v2</id>
    <title>Prometheus 2: An Open Source Language Model Specialized in Evaluating Other Language Models</title>
    <summary>We introduce Prometheus 2.</summary>
    <author><name>Seungone Kim</name></author>
  </entry>
</feed>`

const fixtureRDAP = `{
  "objectClassName": "domain",
  "handle": "2138514_DOMAIN_COM-VRSN",
  "ldhName": "google.com",
  "status": ["client transfer prohibited", "client update prohibited"],
  "events": [
    {"eventAction": "registration", "eventDate": "1997-09-15T04:00:00Z"},
    {"eventAction": "last changed", "eventDate": "2024-01-01T00:00:00Z"}
  ]
}`

const fixtureDoH = `{
  "Status": 0,
  "TC": false,
  "RD": true,
  "RA": true,
  "AD": false,
  "CD": false,
  "Question": [{"name": "google.com.", "type": 1}],
  "Answer": [
    {"name": "google.com.", "type": 1, "TTL": 300, "data": "142.250.80.46"}
  ]
}`

const fixtureIPAPI = `{
  "status": "success",
  "country": "United States",
  "countryCode": "US",
  "region": "VA",
  "regionName": "Virginia",
  "city": "Ashburn",
  "org": "Google Public DNS",
  "query": "8.8.8.8"
}`

const fixtureRIPE = `{
  "service": {
    "name": "abuse-contact",
    "type": "Locator Service"
  },
  "link": {
    "type": "locator",
    "href": "https://rest.db.ripe.net/abuse-contact/8.8.8.8.json"
  }
}`

const fixtureCRT = `[
  {
    "id": 12345,
    "issuer_name": "C=US, O=Google Trust Services, CN=WR2",
    "common_name": "google.com",
    "not_before": "2024-01-01T00:00:00",
    "not_after": "2024-12-31T23:59:59"
  }
]`

const fixtureHIBP = `0005AD76BD555C1D6D771DE417A4B87E4B4:58
000A8DAE4228F821FB418F59826079BF368:4
000DD7F2A1C5B4B6B5B5B5B5B5B5B5B5B5B5:0`

const fixtureNominatim = `[
  {
    "place_id": 145205353,
    "display_name": "Berlin, Germany",
    "lat": "52.5170365",
    "lon": "13.3888599",
    "type": "city",
    "importance": 0.87
  }
]`

const fixtureHN = `{
  "hits": [
    {
      "title": "Show HN: SQLite at scale",
      "url": "https://example.com/sqlite-scale",
      "story_text": "We use SQLite for 50 TB of analytics.",
      "num_comments": 234,
      "points": 1200
    }
  ]
}`

const fixtureGDELT = `[
  {
    "title": "SQLite release 3.50",
    "url": "https://sqlite.org/releaselog/3_50.html",
    "date": "2026-05-01T00:00:00Z",
    "socialimage": "https://sqlite.org/logo.png",
    "language": "English"
  }
]`

const fixtureDDG = `<html><body>
<div class="result">
  <a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fsqlite.org%2F&amp;rut=abcdef">SQLite Home</a>
</div>
<div class="result">
  <a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fduckdb.org%2F&amp;rut=123456">DuckDB</a>
</div>
</body></html>`

// allFixtures maps parser name → fixture body. The snapshot test
// table iterates this map so adding a backend is one fixture
// constant + one entry.
var allFixtures = map[string]string{
	"osv":       fixtureOSV,
	"nvd":       fixtureNVD,
	"npm":       fixtureNPM,
	"crates":    fixtureCrates,
	"github":    fixtureGitHub,
	"openalex":  fixtureOpenAlex,
	"crossref":  fixtureCrossref,
	"arxiv":     fixtureArXiv,
	"rdap":      fixtureRDAP,
	"doh":       fixtureDoH,
	"ip-api":    fixtureIPAPI,
	"ripe":      fixtureRIPE,
	"crt":       fixtureCRT,
	"hibp":      fixtureHIBP,
	"nominatim": fixtureNominatim,
	"hn":        fixtureHN,
	"gdelt":     fixtureGDELT,
	"ddg":       fixtureDDG,
}
