package web

// layout + page templates. Minimal — no JS build, no htmx. Pico.css via CDN
// for plain-but-readable styling.

const layout = `{{ define "layout" }}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Espur admin</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@picocss/pico@2/css/pico.classless.min.css">
  <style>
    body { max-width: 70rem; margin: 2rem auto; padding: 0 1rem; }
    nav a { margin-right: 1rem; }
    table { font-size: 0.9rem; }
    .muted { color: #888; }
    pre { white-space: pre-wrap; word-break: break-word; }
  </style>
</head>
<body>
<header>
  <h1>Espur admin</h1>
  <nav>
    <a href="/">status</a>
    <a href="/vendors">vendors</a>
    <a href="/threads">threads</a>
  </nav>
</header>
<main>
{{ template "page" . }}
</main>
</body>
</html>{{ end }}

{{ define "page" }}
{{ if eq .Page "home" }}{{ template "home" .Data }}
{{ else if eq .Page "vendors" }}{{ template "vendors" .Data }}
{{ else if eq .Page "threads" }}{{ template "threads" .Data }}
{{ else if eq .Page "thread_detail" }}{{ template "thread_detail" .Data }}
{{ end }}
{{ end }}
`

const homeTpl = `{{ define "home" }}
<h2>Status</h2>
<ul>
  <li>Vendors: {{ .NumVendors }} — eligible {{ .NumEligible }} / cooldown {{ .NumCooldown }} / auth-locked {{ .NumAuthLocked }}</li>
  <li>Threads: {{ .NumThreads }}</li>
</ul>
<p>Configure vendors and inspect threads via the nav above.</p>
{{ end }}`

const vendorsTpl = `{{ define "vendors" }}
<h2>Vendors</h2>
<p class="muted">Top of the list is most preferred. Espur always tries from the top.</p>
<table>
  <thead><tr><th>#</th><th>vendor_id</th><th>model</th><th>enabled</th><th>cred</th><th>penalty</th><th>actions</th></tr></thead>
  <tbody>
  {{ range $i, $r := . }}
  <tr>
    <td>{{ $i }}</td>
    <td><code>{{ $r.Vendor.VendorID }}</code></td>
    <td><code>{{ $r.Vendor.Model }}</code></td>
    <td>{{ if $r.Vendor.Enabled }}yes{{ else }}<span class="muted">no</span>{{ end }}</td>
    <td>{{ $r.CredStatus }}</td>
    <td>
      {{ if eq (printf "%s" $r.Penalty.Status) "auth_locked" }}auth-locked
      {{ else if eq (printf "%s" $r.Penalty.Status) "cooldown" }}cooldown until {{ fmtTime $r.Penalty.CooldownUntil }} ({{ untilNow $r.Penalty.CooldownUntil }})
      {{ else }}eligible{{ end }}
    </td>
    <td>
      <form method="post" action="/vendors/reorder" style="display:inline">
        <input type="hidden" name="vendor_id" value="{{ $r.Vendor.VendorID }}">
        <button name="dir" value="up">↑</button>
        <button name="dir" value="down">↓</button>
      </form>
      <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/toggle" style="display:inline">
        <button>toggle</button>
      </form>
      <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/clear-penalty" style="display:inline">
        <button>clear penalty</button>
      </form>
      <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/delete" style="display:inline" onsubmit="return confirm('Delete {{ $r.Vendor.VendorID }}?')">
        <button class="contrast">delete</button>
      </form>
      <details>
        <summary>set key</summary>
        <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/key">
          <label>env var name <input name="env_key" placeholder="ANTHROPIC_API_KEY" required></label>
          <label>API key <input name="key" type="password" required></label>
          <button>save</button>
        </form>
      </details>
    </td>
  </tr>
  {{ else }}
  <tr><td colspan="7" class="muted">no vendors yet — add one below</td></tr>
  {{ end }}
  </tbody>
</table>

<h3>Add vendor</h3>
<form method="post" action="/vendors/add">
  <label>vendor_id <input name="vendor_id" required placeholder="anthropic-byo"></label>
  <label>model <input name="model" required placeholder="anthropic/claude-haiku-4-5"></label>
  <label>env var name (for credentials) <input name="env_key" placeholder="ANTHROPIC_API_KEY"></label>
  <button>add</button>
</form>
{{ end }}`

const threadsTpl = `{{ define "threads" }}
<h2>Threads</h2>
<table>
  <thead><tr><th>platform</th><th>thread (encoded)</th><th>size</th><th>last activity</th><th></th></tr></thead>
  <tbody>
  {{ range . }}
  <tr>
    <td>{{ .Platform }}</td>
    <td><code>{{ .EncID }}</code></td>
    <td>{{ .SizeBytes }} B</td>
    <td>{{ .LastActivity.Format "2006-01-02 15:04 MST" }}</td>
    <td><a href="/threads/{{ .Platform }}/{{ .EncID }}">peek</a></td>
  </tr>
  {{ else }}
  <tr><td colspan="5" class="muted">no threads yet</td></tr>
  {{ end }}
  </tbody>
</table>
{{ end }}`

const threadDetailTpl = `{{ define "thread_detail" }}
<h2>Thread {{ .Platform }} / <code>{{ .EncID }}</code></h2>
<h3>AGENTS.md</h3>
<pre>{{ .Agents }}</pre>
<h3>fact files</h3>
{{ if .Facts }}
<ul>{{ range .Facts }}<li><code>{{ .Name }}</code> — {{ .Size }} B</li>{{ end }}</ul>
{{ else }}<p class="muted">no fact files yet</p>{{ end }}
<h3>recent transcript</h3>
<pre>{{ range .Tail }}{{ .TS }} [{{ .Kind }}] {{ .Author.Label }}: {{ .Body }}
{{ end }}</pre>
{{ end }}`
