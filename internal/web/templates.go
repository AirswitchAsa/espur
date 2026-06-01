package web

// Page templates. Server-rendered HTML matching the Spicadust design system
// shipped under static/css. Layout exposes a status strip + top nav; pages
// supply only their inner markup via {{ template "page" . }}.
//
// The design uses sharp edges, hairlines, no shadows, one accent. Don't
// introduce new color tokens — every style hook routes through ds-* / es-*
// classes that read from --ds and color-role variables.

const layout = `{{ define "layout" }}<!doctype html>
<html lang="en" data-theme="light">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>espur · admin</title>
  <link rel="stylesheet" href="/static/css/colors_and_type.css">
  <link rel="stylesheet" href="/static/css/components.css">
  <link rel="stylesheet" href="/static/css/espur.css">
</head>
<body>
<div class="es-app">
  <nav class="es-nav">
    <a class="es-wordmark" href="/"><span class="es-wordmark__mark"></span>espur_</a>
    <div class="es-nav__links">
      <a class="es-nav-link"{{ if eq .Page "home" }} data-active="true"{{ end }} href="/">Home</a>
      <a class="es-nav-link"{{ if eq .Page "vendors" }} data-active="true"{{ end }} href="/vendors">Vendors</a>
      <a class="es-nav-link"{{ if or (eq .Page "threads") (eq .Page "thread_detail") }} data-active="true"{{ end }} href="/threads">Threads</a>
      <a class="es-nav-link"{{ if eq .Page "oauth" }} data-active="true"{{ end }} href="/oauth">OAuth</a>
      <a class="es-nav-link"{{ if eq .Page "settings" }} data-active="true"{{ end }} href="/settings">Settings</a>
      <a class="es-nav-link"{{ if eq .Page "health" }} data-active="true"{{ end }} href="/health">Health</a>
    </div>
    <div class="es-nav__right">
      <button class="es-iconbtn" id="theme-toggle" aria-label="toggle theme" title="toggle theme"></button>
    </div>
  </nav>
  {{ template "strip" .Strip }}
  <main class="es-main es-scroll">
    {{ template "page" . }}
  </main>
</div>
<script src="/static/js/app.js"></script>
</body>
</html>{{ end }}

{{ define "strip" }}
<div class="es-strip es-scroll">
  {{ range .Adapters }}
  <span class="es-strip__item">
    <span class="es-livedot {{ if .Up }}es-livedot--on{{ else }}es-livedot--off{{ end }}"></span>
    <span class="es-strip__k">{{ .Platform }}</span>
    <span class="es-strip__v">{{ if .Up }}live{{ else }}down{{ end }}</span>
  </span>
  {{ end }}
  <span class="es-strip__item">
    <span class="es-strip__k">pool</span>
    <span class="es-strip__v">{{ .Eligible }} eligible</span>
    {{ if .Cooldown }}<span style="color: var(--warning)">· {{ .Cooldown }} cooldown</span>{{ end }}
    {{ if .Locked }}<span style="color: var(--danger)">· {{ .Locked }} locked</span>{{ end }}
  </span>
  <span class="es-strip__item">
    <span class="es-strip__k">in rotation</span>
    <span class="es-strip__v">{{ if .InRotation }}{{ .InRotation }}{{ else }}—{{ end }}</span>
  </span>
  <span class="es-strip__item" style="margin-left: auto; border-right: 0">
    <span class="es-strip__k">espur</span>
    <span class="es-strip__v">{{ .Version }}</span>
  </span>
</div>
{{ end }}

{{ define "page" }}
{{ if eq .Page "home" }}{{ template "home" .Data }}
{{ else if eq .Page "vendors" }}{{ template "vendors" .Data }}
{{ else if eq .Page "threads" }}{{ template "threads" .Data }}
{{ else if eq .Page "thread_detail" }}{{ template "thread_detail" .Data }}
{{ else if eq .Page "oauth" }}{{ template "oauth" .Data }}
{{ else if eq .Page "settings" }}{{ template "settings" .Data }}
{{ else if eq .Page "health" }}{{ template "health" .Data }}
{{ end }}
{{ end }}
`

// homeTpl renders KPI tiles, the activity feed (recent bot replies aggregated
// from per-thread transcripts by Server.recentFeed; empty state shown only when
// no thread has produced a reply yet), and adapter cards.
const homeTpl = `{{ define "home" }}
<div class="es-page">
  <div class="es-phead">
    <div class="es-phead__l">
      <span class="es-phead__serial">00 / overview</span>
      <h1 class="es-phead__title">Home</h1>
    </div>
  </div>

  <div class="es-grid es-grid--kpi" style="margin-bottom: 16px">
    <div class="es-kpi">
      <div class="es-kpi__label">vendors</div>
      <div class="es-kpi__val">{{ .NumVendors }}<span class="es-kpi__unit">{{ .NumEnabled }} enabled</span></div>
      <div class="es-kpi__sub"><span class="es-kpi__seg">{{ .NumCatalog }} providers in catalog</span></div>
    </div>
    <div class="es-kpi">
      <div class="es-kpi__label">pool status</div>
      <div class="es-kpi__val">{{ .NumEligible }}<span class="es-kpi__unit">eligible</span></div>
      <div class="es-kpi__sub">
        {{ if .NumCooldown }}<span class="es-mini" style="color: var(--warning)">{{ .NumCooldown }} cooldown</span>{{ end }}
        {{ if .NumAuthLocked }}<span class="es-mini" style="color: var(--danger)">{{ .NumAuthLocked }} auth-locked</span>{{ end }}
        {{ if and (not .NumCooldown) (not .NumAuthLocked) }}<span class="es-mini">all clear</span>{{ end }}
      </div>
    </div>
    <div class="es-kpi">
      <div class="es-kpi__label">threads</div>
      <div class="es-kpi__val">{{ .NumThreads }}</div>
      <div class="es-kpi__sub"><span class="es-kpi__seg">across {{ len .Adapters }} adapters</span></div>
    </div>
    <div class="es-kpi">
      <div class="es-kpi__label">uptime</div>
      <div class="es-kpi__val">{{ .Uptime }}</div>
      <div class="es-kpi__sub"><span class="es-kpi__seg">since last restart</span></div>
    </div>
  </div>

  <div class="es-grid es-grid--home">
    <div class="es-card">
      <div class="es-card__head">
        <span class="es-card__title">recent activity</span>
        <span class="es-spec">{{ len .Feed }} events</span>
      </div>
      {{ if .Feed }}
      <div class="es-feed es-scroll" style="max-height: 520px; overflow-y: auto">
        {{ range .Feed }}
        <a class="es-feed__row" href="/threads/{{ .Platform }}/{{ .EncID }}">
          <span class="es-plat">{{ if eq .Platform "discord" }}{{ template "icon-hash" }}{{ else }}{{ template "icon-msg" }}{{ end }}</span>
          <div class="es-feed__main">
            <span class="es-feed__thread">{{ .ThreadLabel }}</span>
            <span class="es-feed__vendor">{{ if .Vendor }}{{ .Vendor }}{{ if .Outcome }} · {{ end }}{{ end }}{{ if .Outcome }}<span{{ if ne .Outcome "success" }} style="color: var(--danger)"{{ end }}>{{ .Outcome }}</span>{{ end }}</span>
          </div>
          <div class="es-feed__meta">
            <span>{{ .Ago }}</span>
          </div>
        </a>
        {{ end }}
      </div>
      {{ else }}
      <div class="es-empty">
        <div class="es-empty__glyph">{{ template "icon-inbox" }}</div>
        <div class="es-empty__title">No activity yet</div>
        <div class="es-empty__sub">Invocations will appear here once your bot starts handling messages.</div>
      </div>
      {{ end }}
    </div>

    <div style="display: flex; flex-direction: column; gap: 12px">
      <div class="es-sectionhead"><span class="es-sectionhead__serial">01</span><span class="es-sectionhead__title">Adapters</span></div>
      {{ range .Adapters }}
      <div class="es-adapter">
        <div class="es-adapter__top">
          <span class="es-adapter__name">
            <span class="es-plat">{{ if eq .Platform "discord" }}{{ template "icon-hash" }}{{ else }}{{ template "icon-msg" }}{{ end }}</span>
            {{ .Platform }}
          </span>
          <span class="es-status {{ if .Up }}es-status--ok{{ else }}es-status--danger{{ end }}">
            <span class="es-status__dot"></span>{{ if .Up }}connected{{ else }}disconnected{{ end }}
          </span>
        </div>
        <div class="es-adapter__stats">
          <div class="es-adapter__stat">
            <span class="es-adapter__statk">threads</span>
            <span class="es-adapter__statv">{{ .Threads }}</span>
          </div>
          <div class="es-adapter__stat">
            <span class="es-adapter__statk">status</span>
            <span class="es-adapter__statv">{{ if .Up }}live{{ else }}reconnecting{{ end }}</span>
          </div>
        </div>
      </div>
      {{ else }}
      <div class="es-card" style="padding: 24px"><span class="es-empty__sub">No adapters registered.</span></div>
      {{ end }}

      <div class="es-card" style="padding: 16px">
        <div class="es-card__title" style="margin-bottom: 12px">current rotation</div>
        {{ if .Rotation }}
        <div style="display: flex; flex-direction: column; gap: 2px">
          {{ range $i, $v := .Rotation }}
          <div style="display: flex; align-items: center; gap: 10px; padding: 5px 0; border-bottom: 1px solid var(--line)">
            <span class="es-pri" style="width: 18px; color: {{ if $v.Ready }}var(--ink){{ else }}var(--muted){{ end }}">{{ inc $i }}</span>
            <span style="flex: 1; font-family: var(--ds-font-mono); font-size: 12px; color: {{ if $v.Ready }}var(--ink){{ else }}var(--muted){{ end }}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">{{ $v.VendorID }}</span>
            {{ if $v.Ready }}<span class="es-livedot es-livedot--on"></span>{{ else }}<span class="es-status es-status--muted" style="font-size: 11px">{{ $v.Why }}</span>{{ end }}
          </div>
          {{ end }}
        </div>
        {{ else }}
        <span class="es-empty__sub">No vendors configured.</span>
        {{ end }}
      </div>
    </div>
  </div>
</div>
{{ end }}`

// vendorsTpl: priority list with drag reorder, add-vendor drawer, inline
// set-key panel, delete confirm modal. Mutation endpoints unchanged from v0.1.
const vendorsTpl = `{{ define "vendors" }}
<div class="es-page">
  <div class="es-phead">
    <div class="es-phead__l">
      <span class="es-phead__serial">02 / pool</span>
      <h1 class="es-phead__title">Vendors</h1>
      <span class="es-phead__sub">Drag to reorder. Invocations try vendors top-down, skipping anything disabled, missing a key, in cooldown, or auth-locked.</span>
    </div>
    <div class="es-phead__r">
      <button class="ds-btn ds-btn--action" data-open="add-vendor-overlay">{{ template "icon-plus" }}<span class="ds-btn__label">Add vendor</span></button>
    </div>
  </div>

  {{ if .Rows }}
  <form id="reorder-form" method="post" action="/vendors/reorder-all" style="margin: 0"></form>
  <div class="es-card">
    <div class="es-thead" style="grid-template-columns: 20px 32px minmax(150px,1.5fr) minmax(120px,1.1fr) 104px 60px 96px 156px 40px 34px">
      <span></span><span>#</span><span>vendor</span><span>model</span><span>provider</span>
      <span>cred</span><span>key</span><span>penalty</span><span>on</span><span></span>
    </div>
    <div id="vendor-rows">
    {{ range $i, $r := .Rows }}
    <div class="es-trow" data-vid="{{ $r.Vendor.VendorID }}" style="grid-template-columns: 20px 32px minmax(150px,1.5fr) minmax(120px,1.1fr) 104px 60px 96px 156px 40px 34px">
      <span class="es-grip" title="drag to reorder">{{ template "icon-grip" }}</span>
      <span class="es-pri">{{ inc $i }}</span>
      <span class="es-cell-strong" title="{{ $r.Vendor.VendorID }}">{{ $r.Vendor.VendorID }}</span>
      <span class="es-cell-mono" title="{{ $r.Vendor.Model }}">{{ $r.Vendor.Model }}</span>
      <span><span class="es-prov" title="{{ $r.ProviderName }}">{{ $r.ProviderShort }}</span></span>
      <span><span class="ds-tag ds-tag--block">{{ if eq $r.Vendor.CredKind "oauth" }}OAuth{{ else }}BYO{{ end }}</span></span>
      <span>
        {{ if eq $r.CredStatus "linked" }}
        <span class="es-status es-status--ok"><span class="es-status__dot"></span>linked</span>
        {{ else if eq $r.CredStatus "pending" }}
        <span class="es-status es-status--warn" title="run: opencode auth login --provider {{ $r.ProviderName }}"><span class="es-status__dot"></span>auth pending</span>
        {{ else if eq $r.CredStatus "set" }}
        <span class="es-status es-status--ok"><span class="es-status__dot"></span>set</span>
        {{ else }}
        <span class="es-status es-status--warn"><span class="es-status__dot"></span>missing</span>
        {{ end }}
      </span>
      <span>
        {{ if eq $r.PenaltyKind "auth_locked" }}
        <span class="es-countdown es-countdown--locked">{{ template "icon-lock-sm" }}auth-locked</span>
        {{ else if and (eq $r.PenaltyKind "cooldown") (gt $r.CooldownUntilUnix 0) }}
        <span class="es-countdown" data-cooldown-until="{{ $r.CooldownUntilUnix }}" title="cooldown remaining">
          {{ template "icon-clock-sm" }}<span class="es-countdown-val">{{ $r.CooldownRemaining }}</span>
        </span>
        {{ else }}
        <span class="es-status es-status--ok"><span class="es-status__dot"></span>eligible</span>
        {{ end }}
      </span>
      <span>
        <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/toggle" style="display: inline">
          <button type="submit" class="ds-toggle" aria-checked="{{ if $r.Vendor.Enabled }}true{{ else }}false{{ end }}" aria-label="toggle vendor"></button>
        </form>
      </span>
      <span class="es-menu-wrap">
        <button class="es-iconbtn" data-menu-toggle aria-label="row actions">{{ template "icon-kebab" }}</button>
        <div class="es-menu" style="display: none">
          {{ if eq $r.Vendor.CredKind "byo_key" }}
          <button type="button" data-setkey-toggle="{{ $r.Vendor.VendorID }}">{{ template "icon-key-sm" }}{{ if eq $r.CredStatus "set" }}Replace key{{ else }}Set key{{ end }}</button>
          {{ end }}
          <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/clear-penalty" style="margin: 0">
            <button type="submit">{{ template "icon-shield" }}Clear penalty</button>
          </form>
          <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/toggle" style="margin: 0">
            <button type="submit">{{ template "icon-power" }}{{ if $r.Vendor.Enabled }}Disable{{ else }}Enable{{ end }}</button>
          </form>
          <div class="es-menu__sep"></div>
          <button type="button" class="es-menu--danger" data-open="delete-{{ $r.Vendor.VendorID }}">{{ template "icon-trash" }}Delete vendor</button>
        </div>
      </span>
    </div>
    {{ if eq $r.Vendor.CredKind "byo_key" }}
    <div class="es-inline" id="setkey-{{ $r.Vendor.VendorID }}" hidden>
      <div class="es-inline__label">{{ if eq $r.CredStatus "set" }}replace key{{ else }}set key{{ end }} · {{ $r.Vendor.VendorID }}</div>
      <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/key" class="es-inline__row">
        <input type="hidden" name="env_key" value="{{ $r.EnvKey }}">
        <div class="es-secret">
          {{ template "icon-key-sm" }}
          <input type="password" name="key" id="key-input-{{ $r.Vendor.VendorID }}" placeholder="{{ if $r.EnvKey }}{{ if eq $r.CredStatus "set" }}new {{ end }}{{ $r.EnvKey }} value{{ else }}secret value{{ end }}" required>
          <button type="button" class="es-secret__btn" data-secret-toggle="key-input-{{ $r.Vendor.VendorID }}" aria-label="toggle visibility">{{ template "icon-eye" }}</button>
        </div>
        <button type="submit" class="ds-btn ds-btn--ink ds-btn--sm"><span class="ds-btn__label">{{ if eq $r.CredStatus "set" }}Replace{{ else }}Save{{ end }} key</span></button>
        <button type="button" class="ds-btn ds-btn--ghost ds-btn--sm" data-setkey-toggle="{{ $r.Vendor.VendorID }}"><span class="ds-btn__label">Cancel</span></button>
      </form>
      <div class="es-hint">{{ if eq $r.CredStatus "set" }}Replaces the stored credential.{{ else }}Stored encrypted at rest.{{ end }} Written to <code>{{ $r.EnvKey }}</code> at invocation time. Never displayed again after saving.</div>
    </div>
    {{ end }}

    <div class="es-overlay" id="delete-{{ $r.Vendor.VendorID }}" hidden>
      <div class="es-scrim" data-close="delete-{{ $r.Vendor.VendorID }}"></div>
      <div class="es-modal" role="dialog" aria-modal="true">
        <div class="es-modal__head">{{ template "icon-alert" }}<span class="es-modal__title">Delete vendor</span></div>
        <div class="es-modal__body">
          Removing <strong>{{ $r.Vendor.VendorID }}</strong> takes it out of rotation permanently. Any stored credential for this vendor is also discarded.
          <div class="es-modal__detail">{{ $r.ProviderName }} · {{ $r.Vendor.Model }}</div>
        </div>
        <div class="es-modal__foot">
          <button type="button" class="ds-btn ds-btn--ghost" data-close="delete-{{ $r.Vendor.VendorID }}"><span class="ds-btn__label">Cancel</span></button>
          <form method="post" action="/vendors/{{ $r.Vendor.VendorID }}/delete" style="margin: 0">
            <button type="submit" class="ds-btn ds-btn--danger-filled">{{ template "icon-trash" }}<span class="ds-btn__label">Delete vendor</span></button>
          </form>
        </div>
      </div>
    </div>
    {{ end }}
    </div>
  </div>
  {{ else }}
  <div class="es-card">
    <div class="es-empty">
      <div class="es-empty__glyph">{{ template "icon-box" }}</div>
      <div class="es-empty__title">No vendors yet</div>
      <div class="es-empty__sub">A vendor pairs a model with a credential. Add your first one to start routing invocations.</div>
      <button class="ds-btn ds-btn--action" data-open="add-vendor-overlay">{{ template "icon-plus" }}<span class="ds-btn__label">Add vendor</span></button>
    </div>
  </div>
  {{ end }}

  {{ template "add-vendor-drawer" . }}
</div>
{{ end }}`

const addVendorDrawerTpl = `{{ define "add-vendor-drawer" }}
<div class="es-overlay" id="add-vendor-overlay" hidden>
  <div class="es-scrim" data-close="add-vendor-overlay"></div>
  <form method="post" action="/vendors/add" class="es-drawer" role="dialog" aria-modal="true">
    <div class="es-drawer__head">
      <div>
        <span class="es-drawer__serial">02 · new vendor</span>
        <span class="es-drawer__title">Add vendor</span>
      </div>
      <button type="button" class="es-iconbtn" data-close="add-vendor-overlay" aria-label="close">{{ template "icon-x" }}</button>
    </div>
    <div class="es-drawer__body es-scroll">
      <div class="es-form">
        <div class="es-fieldgrp">
          <label class="es-label">model</label>
          <select class="es-select" name="model" id="model-select" required>
            {{ range .Providers }}
            <optgroup label="{{ .Name }}">
              {{ $p := . }}
              {{ range .Models }}
              <option value="{{ $p.ID }}/{{ . }}"
                      data-provider="{{ $p.ID }}"
                      data-provider-name="{{ $p.Name }}"
                      data-env="{{ $p.EnvKey }}"
                      data-oauth="{{ if $p.SupportsOAuth }}1{{ else }}0{{ end }}">{{ . }}</option>
              {{ end }}
            </optgroup>
            {{ end }}
          </select>
          <span class="es-hint">Provider: <strong id="provider-hint" style="color: var(--ink)"></strong></span>
        </div>

        <div class="es-fieldgrp">
          <label class="es-label">credential source</label>
          <div class="es-choice">
            <label class="es-choice__opt" id="cred-byo-opt" data-cred-opt="byo_key" data-sel="true">
              <input type="radio" name="cred_kind" value="byo_key" id="cred-byo" checked style="display: none">
              <span class="es-radio"></span>
              <span class="es-choice__txt">
                <span class="es-choice__name">BYO key</span>
                <span class="es-choice__desc">paste an API key</span>
              </span>
            </label>
            <label class="es-choice__opt" id="cred-oauth-opt" data-cred-opt="oauth">
              <input type="radio" name="cred_kind" value="oauth" id="cred-oauth" style="display: none">
              <span class="es-radio"></span>
              <span class="es-choice__txt">
                <span class="es-choice__name">OAuth</span>
                <span class="es-choice__desc" id="cred-oauth-desc">via opencode session</span>
              </span>
            </label>
          </div>
          <div class="es-fieldgrp" style="margin-top: 6px">
            <label class="es-label">env var</label>
            <input class="es-text es-text--mono" id="env-field" readonly>
            <span class="es-hint" id="cred-hint">The key you set is written to this variable at invocation time.</span>
          </div>
        </div>

        <div class="es-fieldgrp">
          <label class="es-label">vendor id</label>
          <input class="es-text es-text--mono" name="vendor_id" id="vendor-id-input" required>
          <span class="es-hint">A unique handle for this entry. Defaults from the model name; edit freely.</span>
        </div>
      </div>
    </div>
    <div class="es-drawer__foot">
      <button type="button" class="ds-btn ds-btn--ghost" data-close="add-vendor-overlay"><span class="ds-btn__label">Cancel</span></button>
      <button type="submit" class="ds-btn ds-btn--action">{{ template "icon-plus" }}<span class="ds-btn__label">Add to pool</span></button>
    </div>
  </form>
</div>
{{ end }}`

const threadsTpl = `{{ define "threads" }}
<div class="es-page">
  <div class="es-phead">
    <div class="es-phead__l">
      <span class="es-phead__serial">03 / conversations</span>
      <h1 class="es-phead__title">Threads</h1>
      <span class="es-phead__sub">Every conversation across adapters. Each owns a workdir and its own memory.</span>
    </div>
  </div>

  <div class="es-filters">
    <span class="es-spec" style="margin-left: auto">{{ len . }} threads</span>
  </div>

  {{ if . }}
  <div class="es-card">
    <div class="es-thead" style="grid-template-columns: 28px minmax(160px,1.6fr) 130px 100px">
      <span></span><span>thread</span><span>last activity</span><span>size</span>
    </div>
    {{ range . }}
    <a class="es-trow es-trow--click" style="grid-template-columns: 28px minmax(160px,1.6fr) 130px 100px; text-decoration: none; color: inherit" href="/threads/{{ .Platform }}/{{ .EncID }}">
      <span class="es-plat">{{ if eq .Platform "discord" }}{{ template "icon-hash" }}{{ else }}{{ template "icon-msg" }}{{ end }}</span>
      <div style="min-width: 0">
        <div class="es-cell-strong">{{ .Platform }}</div>
        <div class="es-cell-mono" style="color: var(--muted)">{{ .EncID }}</div>
      </div>
      <span class="es-cell-muted">{{ .LastActivityFmt }}</span>
      <span class="es-cell-mono">{{ .SizeFmt }}</span>
    </a>
    {{ end }}
  </div>
  {{ else }}
  <div class="es-card">
    <div class="es-empty">
      <div class="es-empty__glyph">{{ template "icon-msg" }}</div>
      <div class="es-empty__title">No threads yet</div>
      <div class="es-empty__sub">Threads appear here once a bot adapter receives its first message.</div>
    </div>
  </div>
  {{ end }}
</div>
{{ end }}`

const threadDetailTpl = `{{ define "thread_detail" }}
<div class="es-page" style="max-width: 1100px">
  <a class="ds-nav-link" style="margin-bottom: 14px" href="/threads">{{ template "icon-chev-l" }} threads</a>

  <div class="es-phead" style="align-items: flex-start; margin-bottom: 18px">
    <div class="es-phead__l">
      <span class="es-phead__serial" style="display: inline-flex; align-items: center; gap: 8px">
        <span class="es-plat">{{ if eq .Platform "discord" }}{{ template "icon-hash" }}{{ else }}{{ template "icon-msg" }}{{ end }}</span> {{ .Platform }}
      </span>
      <h1 class="es-phead__title">{{ .EncID }}</h1>
      <div style="display: flex; gap: 22px; margin-top: 6px; flex-wrap: wrap">
        <span style="display: inline-flex; align-items: center; gap: 8px">
          <span class="es-spec">workdir</span>
          <span class="es-copyrow">
            <span class="es-copyval">{{ .Workdir }}</span>
            <button class="es-copybtn" data-copy="{{ .Workdir }}" title="copy" aria-label="copy">{{ template "icon-copy" }}</button>
          </span>
        </span>
      </div>
    </div>
    <div class="es-phead__r">
      <span class="es-spec">{{ .Turns }} turns</span>
    </div>
  </div>

  <div class="es-tabs" style="padding: 0" data-tab-group="thread">
    <button type="button" class="es-tab" data-tab="transcript" data-active="true">{{ template "icon-msg" }} Transcript</button>
    <button type="button" class="es-tab" data-tab="memory">{{ template "icon-file" }} Memory <span class="es-tab__count">{{ inc (len .Facts) }}</span></button>
    <button type="button" class="es-tab" data-tab="workdir">{{ template "icon-folder" }} Workdir files</button>
  </div>

  <div style="padding-top: 18px" data-tab-pane="transcript" data-pane-group="thread">
    {{ if .Tail }}
    {{ range .Tail }}
    <div class="es-turn">
      <div class="es-turn__role es-turn__role--{{ if eq .Kind "user" }}user{{ else }}bot{{ end }}">{{ if eq .Kind "user" }}{{ if .Author.Label }}{{ .Author.Label }}{{ else }}user{{ end }}{{ else }}espur{{ end }}</div>
      <div>
        <div class="es-turn__body">{{ .Body }}</div>
        <div class="es-turn__meta">{{ .TSFmt }}</div>
      </div>
    </div>
    {{ end }}
    {{ else }}
    <div class="es-empty">
      <div class="es-empty__glyph">{{ template "icon-msg" }}</div>
      <div class="es-empty__title">No transcript yet</div>
      <div class="es-empty__sub">Messages will appear here once this thread sees activity.</div>
    </div>
    {{ end }}
  </div>

  <div style="padding-top: 18px" data-tab-pane="memory" data-pane-group="thread" hidden>
    <form method="post" action="/threads/{{ .Platform }}/{{ .EncID }}/instructions" class="es-card" style="margin: 0">
      <div class="es-card__head">
        <div>
          <div class="es-card__title">Custom instructions</div>
          <div class="es-hint">Persona, tone, do/don't rules for this thread. The bot reads these alongside its built-in memory rules on every invocation. Empty is fine.</div>
        </div>
        <button type="submit" class="ds-btn ds-btn--ink ds-btn--sm">{{ template "icon-check-lg" }}<span class="ds-btn__label">Save</span></button>
      </div>
      <div class="es-card__body" style="padding: 0">
        <textarea name="body" class="es-text--area" spellcheck="false" placeholder="# House rules for this thread&#10;&#10;- Speak in the persona of …&#10;- Never use emoji&#10;- When asked about X, defer to …">{{ .Instructions }}</textarea>
      </div>
    </form>

    {{ if .Facts }}
    <div class="es-card" style="margin-top: 16px; padding: 0">
      <div class="es-card__head">
        <div>
          <div class="es-card__title">Bot memory</div>
          <div class="es-hint">What the bot has stored about this thread on its own — the index and slug files it manages. Read-only here.</div>
        </div>
      </div>
      <div class="es-tree-pane" data-picker="mem" style="border: 0">
        <div class="es-tree-side es-scroll">
          <ul class="ds-tree">
            {{ range $i, $f := .Facts }}
            <li>
              <button type="button" class="ds-tree__row" {{ if eq $i 0 }}aria-current="true"{{ end }} data-file-pick="{{ $f.Name }}">
                <span class="ds-tree__indicator" data-kind="leaf"></span>
                <span class="ds-tree__label">{{ $f.Name }}</span>
                <span class="ds-tree__meta">{{ $f.SizeFmt }}</span>
              </button>
            </li>
            {{ end }}
          </ul>
        </div>
        <div class="es-tree-view es-scroll" data-file-view="mem">
          {{ with index .Facts 0 }}
          <pre style="font-family: var(--ds-font-mono); white-space: pre-wrap; word-break: break-word; color: var(--ink); font-size: 13px; line-height: 1.6; margin: 0">{{ .Body }}</pre>
          {{ end }}
        </div>
        {{ range .Facts }}
        <template data-file-body="mem-{{ .EscapedName }}"><pre style="font-family: var(--ds-font-mono); white-space: pre-wrap; word-break: break-word; color: var(--ink); font-size: 13px; line-height: 1.6; margin: 0">{{ .Body }}</pre></template>
        {{ end }}
      </div>
    </div>
    {{ end }}
  </div>

  <div style="padding-top: 18px" data-tab-pane="workdir" data-pane-group="thread" hidden>
    {{ if .WorkdirFiles }}
    <div class="es-card" style="padding: 0">
      <div class="es-thead" style="grid-template-columns: 24px 1fr 100px 140px">
        <span></span><span>name</span><span>size</span><span>modified</span>
      </div>
      {{ range .WorkdirFiles }}
      <div class="es-trow" style="grid-template-columns: 24px 1fr 100px 140px">
        <span style="color: var(--muted)">{{ if .IsDir }}{{ template "icon-folder" }}{{ else }}{{ template "icon-file" }}{{ end }}</span>
        <span class="es-cell-mono">{{ .Name }}</span>
        <span class="es-cell-mono">{{ if .IsDir }}—{{ else }}{{ .SizeFmt }}{{ end }}</span>
        <span class="es-cell-muted">{{ .ModTimeFmt }}</span>
      </div>
      {{ end }}
    </div>
    {{ else }}
    <div class="es-empty">
      <div class="es-empty__glyph">{{ template "icon-folder" }}</div>
      <div class="es-empty__title">Empty workdir</div>
      <div class="es-empty__sub">No files have been written to this thread's workdir yet.</div>
    </div>
    {{ end }}
  </div>

  <div class="es-danger">
    <div class="es-danger__head">danger zone</div>
    <div class="es-danger__row">
      <div class="es-danger__txt">
        <span class="es-danger__name">Reset bot memory</span>
        <span class="es-danger__desc">Deletes the bot's remembered facts for this thread — <code>memory_index.md</code> and its per-fact slug files. Your custom instructions (<code>AGENTS.md</code>) and the transcript are kept.</span>
      </div>
      <button class="ds-btn ds-btn--danger ds-btn--sm" data-open="confirm-wipe">{{ template "icon-trash" }}<span class="ds-btn__label">Reset memory</span></button>
    </div>
    <div class="es-danger__row">
      <div class="es-danger__txt">
        <span class="es-danger__name">Force-delete workdir</span>
        <span class="es-danger__desc">Permanently remove the entire thread workdir, including memory and artifacts.</span>
      </div>
      <button class="ds-btn ds-btn--danger ds-btn--sm" data-open="confirm-delete">{{ template "icon-trash" }}<span class="ds-btn__label">Delete workdir</span></button>
    </div>
  </div>

  <div class="es-overlay" id="confirm-wipe" hidden>
    <div class="es-scrim" data-close="confirm-wipe"></div>
    <div class="es-modal" role="dialog" aria-modal="true">
      <div class="es-modal__head">{{ template "icon-alert" }}<span class="es-modal__title">Reset bot memory</span></div>
      <div class="es-modal__body">
        This deletes the bot's memory files — <strong>memory_index.md</strong> and its slug <code>*.md</code> fact files. Your custom instructions in <code>AGENTS.md</code> and the transcript stay put.
        <div class="es-modal__detail">{{ .Workdir }}/*.md (except AGENTS.md)</div>
      </div>
      <div class="es-modal__foot">
        <button type="button" class="ds-btn ds-btn--ghost" data-close="confirm-wipe"><span class="ds-btn__label">Cancel</span></button>
        <form method="post" action="/threads/{{ .Platform }}/{{ .EncID }}/wipe-memory" style="margin: 0">
          <button type="submit" class="ds-btn ds-btn--danger-filled">{{ template "icon-trash" }}<span class="ds-btn__label">Reset memory</span></button>
        </form>
      </div>
    </div>
  </div>

  <div class="es-overlay" id="confirm-delete" hidden>
    <div class="es-scrim" data-close="confirm-delete"></div>
    <div class="es-modal" role="dialog" aria-modal="true">
      <div class="es-modal__head">{{ template "icon-alert" }}<span class="es-modal__title">Force-delete workdir</span></div>
      <div class="es-modal__body">
        This permanently removes the entire workdir. Memory, artifacts, and notes are unrecoverable.
        <div class="es-modal__detail">{{ .Workdir }}</div>
      </div>
      <div class="es-modal__foot">
        <button type="button" class="ds-btn ds-btn--ghost" data-close="confirm-delete"><span class="ds-btn__label">Cancel</span></button>
        <form method="post" action="/threads/{{ .Platform }}/{{ .EncID }}/delete" style="margin: 0">
          <button type="submit" class="ds-btn ds-btn--danger-filled">{{ template "icon-trash" }}<span class="ds-btn__label">Delete everything</span></button>
        </form>
      </div>
    </div>
  </div>
</div>
{{ end }}`

const oauthTpl = `{{ define "oauth" }}
<div class="es-page" style="max-width: 1000px">
  <div class="es-phead">
    <div class="es-phead__l">
      <span class="es-phead__serial">04 / credentials</span>
      <h1 class="es-phead__title">OAuth</h1>
      <span class="es-phead__sub">Sessions managed by <code>opencode auth login</code>. Login is terminal-only; espur reads the resulting session.</span>
    </div>
  </div>

  <div style="display: flex; flex-direction: column; gap: 12px">
    {{ range .Entries }}
    <div class="es-card">
      <div style="display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 16px 18px">
        <div style="display: flex; align-items: center; gap: 14px; min-width: 0">
          <span style="width: 36px; height: 36px; border: 1px solid var(--line-strong); border-radius: 50%; display: flex; align-items: center; justify-content: center; color: {{ if .HasKey }}var(--system){{ else }}var(--muted){{ end }}; flex-shrink: 0">
            {{ if .HasKey }}{{ template "icon-shield-lg" }}{{ else }}{{ template "icon-lock-lg" }}{{ end }}
          </span>
          <div style="min-width: 0">
            <div style="font-size: var(--ds-text-md); font-weight: 500; color: var(--ink)">{{ .Provider }}</div>
            <div style="display: flex; gap: 16px; margin-top: 3px">
              {{ if .HasKey }}
              <span class="es-status es-status--ok"><span class="es-status__dot"></span>signed in</span>
              {{ else }}
              <span class="es-status es-status--warn"><span class="es-status__dot"></span>not signed in</span>
              {{ end }}
              <span class="es-spec" style="align-self: center">{{ .Type }}</span>
            </div>
          </div>
        </div>
      </div>
      {{ if not .HasKey }}
      <div style="border-top: 1px solid var(--line); padding: 14px 18px; background: var(--paper-soft)">
        <div class="es-spec" style="margin-bottom: 8px">sign in — run on the box</div>
        <div class="es-snippet">
          <code>docker exec -it espur opencode auth login {{ .Provider }}</code>
          <button class="es-copybtn" data-copy="docker exec -it espur opencode auth login {{ .Provider }}" title="copy" aria-label="copy">{{ template "icon-copy" }}</button>
        </div>
        <div class="es-hint" style="margin-top: 8px">Then reload this page to pick up the new session.</div>
      </div>
      {{ end }}
    </div>
    {{ else }}
    <div class="es-card">
      <div class="es-empty">
        <div class="es-empty__glyph">{{ template "icon-key" }}</div>
        <div class="es-empty__title">No OAuth providers configured</div>
        <div class="es-empty__sub">Run <code>opencode auth login &lt;provider&gt;</code> inside the espur container to authorise one.</div>
      </div>
    </div>
    {{ end }}
  </div>

  <div class="es-card" style="margin-top: 18px; padding: 16px">
    <div class="es-card__title" style="margin-bottom: 8px">where these live</div>
    <div class="es-hint">opencode writes the session bundle to <code>{{ .AuthPath }}</code>. Espur's child invocations read it automatically — no env vars to set for OAuth vendors.</div>
  </div>
</div>
{{ end }}`

const settingsTpl = `{{ define "settings" }}
<div class="es-page" style="max-width: 980px">
  <div class="es-phead">
    <div class="es-phead__l">
      <span class="es-phead__serial">05 / configuration</span>
      <h1 class="es-phead__title">Settings</h1>
      <span class="es-phead__sub">Operator knobs. Read from the running process — change them via env vars or config and restart.</span>
    </div>
  </div>

  <div class="es-banner">
    <span class="es-banner__icon">{{ template "icon-key" }}</span>
    <div class="es-banner__txt">
      <strong>Master key reminder.</strong> The encryption key for stored credentials lives only on this box. If you haven't already, back it up off-box — losing it means re-entering every BYO key.
    </div>
  </div>

  <div class="es-set-grid">
    <div class="es-set-block">
      <div class="es-set-block__head"><span class="es-set-block__title">invocation</span></div>
      <div class="es-set-block__body">
        <div class="es-set-row">
          <div class="es-set-row__l"><span class="es-set-row__name">Transcript tail</span><span class="es-set-row__hint">turns of context sent per invocation</span></div>
          <div class="es-set-row__r"><span class="es-cell-mono">{{ .TranscriptTail }}</span><span class="es-spec">turns</span></div>
        </div>
        <div class="es-set-row">
          <div class="es-set-row__l"><span class="es-set-row__name">Max context bytes</span><span class="es-set-row__hint">cap on the assembled prompt body</span></div>
          <div class="es-set-row__r"><span class="es-cell-mono">{{ .MaxBytesFmt }}</span></div>
        </div>
        <div class="es-set-row">
          <div class="es-set-row__l"><span class="es-set-row__name">Max AGENTS.md bytes</span><span class="es-set-row__hint">truncation guardrail for memory inlining</span></div>
          <div class="es-set-row__r"><span class="es-cell-mono">{{ .MaxAgentsMDFmt }}</span></div>
        </div>
      </div>
    </div>

    <div class="es-set-block">
      <div class="es-set-block__head"><span class="es-set-block__title">runtime</span></div>
      <div class="es-set-block__body">
        <div class="es-set-row">
          <div class="es-set-row__l"><span class="es-set-row__name">Version</span><span class="es-set-row__hint">espur binary version</span></div>
          <div class="es-set-row__r"><span class="es-cell-mono">{{ .Version }}</span></div>
        </div>
        <div class="es-set-row">
          <div class="es-set-row__l"><span class="es-set-row__name">Data dir</span><span class="es-set-row__hint">where threads and the database live</span></div>
          <div class="es-set-row__r"><span class="es-cell-mono">{{ .DataDir }}</span></div>
        </div>
        <div class="es-set-row">
          <div class="es-set-row__l"><span class="es-set-row__name">Admin bind</span><span class="es-set-row__hint">where this UI is served</span></div>
          <div class="es-set-row__r"><span class="es-cell-mono">{{ .AdminBind }}</span></div>
        </div>
      </div>
    </div>

    <div class="es-set-block" style="grid-column: 1 / -1">
      <div class="es-set-block__head"><span class="es-set-block__title">adapters</span><span class="es-restart-tag">restart to change</span></div>
      <div class="es-set-block__body">
        {{ range .Adapters }}
        <div class="es-set-row">
          <div class="es-set-row__l">
            <span class="es-set-row__name">
              <span class="es-plat">{{ if eq .Platform "discord" }}{{ template "icon-hash" }}{{ else }}{{ template "icon-msg" }}{{ end }}</span>
              {{ .Platform }}
            </span>
            <span class="es-set-row__hint">{{ if .Up }}connected{{ else }}disconnected{{ end }}</span>
          </div>
          <div class="es-set-row__r">
            <span class="es-status {{ if .Up }}es-status--ok{{ else }}es-status--danger{{ end }}">
              <span class="es-status__dot"></span>{{ if .Up }}live{{ else }}down{{ end }}
            </span>
          </div>
        </div>
        {{ else }}
        <div class="es-empty__sub" style="padding: 16px 0">No adapters registered.</div>
        {{ end }}
      </div>
    </div>
  </div>
</div>
{{ end }}`

const healthTpl = `{{ define "health" }}
<div class="es-page" style="max-width: 1000px">
  <div class="es-phead">
    <div class="es-phead__l">
      <span class="es-phead__serial">06 / system</span>
      <h1 class="es-phead__title">Health</h1>
      <span class="es-phead__sub">Human-readable companion to <code>/healthz</code>.</span>
    </div>
  </div>

  <div style="display: flex; align-items: center; gap: 12px; margin-bottom: 18px">
    <span style="width: 44px; height: 44px; border-radius: 50%; background: color-mix(in srgb, {{ if .OK }}var(--system){{ else }}var(--danger){{ end }}, transparent 86%); color: {{ if .OK }}var(--system){{ else }}var(--danger){{ end }}; display: flex; align-items: center; justify-content: center">
      {{ if .OK }}{{ template "icon-check-lg" }}{{ else }}{{ template "icon-alert-lg" }}{{ end }}
    </span>
    <div>
      <div style="font-size: var(--ds-text-xl); font-weight: 500; color: var(--ink); letter-spacing: -0.01em">{{ if .OK }}All systems operational{{ else }}Degraded{{ end }}</div>
      <div class="es-spec">{{ .Version }} · uptime {{ .Uptime }}</div>
    </div>
  </div>

  <div class="es-health-grid">
    <div class="es-card" style="padding: 16px; display: flex; flex-direction: column; gap: 10px">
      <div style="display: flex; align-items: center; justify-content: space-between">
        <span class="es-kpi__label">adapters</span>
        <span style="color: {{ if .AllAdaptersUp }}var(--system){{ else }}var(--warning){{ end }}">{{ template "icon-msg" }}</span>
      </div>
      <div style="font-size: var(--ds-text-lg); font-weight: 500; color: var(--ink)">{{ .NumAdaptersUp }} / {{ .NumAdaptersTotal }} connected</div>
      <div class="es-spec">{{ .AdapterList }}</div>
    </div>

    <div class="es-card" style="padding: 16px; display: flex; flex-direction: column; gap: 10px">
      <div style="display: flex; align-items: center; justify-content: space-between">
        <span class="es-kpi__label">database</span>
        <span style="color: var(--system)">{{ template "icon-db" }}</span>
      </div>
      <div style="font-size: var(--ds-text-lg); font-weight: 500; color: var(--ink)">{{ .DBStatus }}</div>
      <div class="es-spec">sqlite</div>
    </div>

    <div class="es-card" style="padding: 16px; display: flex; flex-direction: column; gap: 10px">
      <div style="display: flex; align-items: center; justify-content: space-between">
        <span class="es-kpi__label">pool</span>
        <span style="color: {{ if .NumAuthLocked }}var(--danger){{ else if .NumCooldown }}var(--warning){{ else }}var(--system){{ end }}">{{ template "icon-shield-lg" }}</span>
      </div>
      <div style="font-size: var(--ds-text-lg); font-weight: 500; color: var(--ink)">{{ .NumEligible }} eligible</div>
      <div class="es-spec">{{ .NumCooldown }} cooldown · {{ .NumAuthLocked }} locked</div>
    </div>

    <div class="es-card" style="padding: 16px; display: flex; flex-direction: column; gap: 10px">
      <div style="display: flex; align-items: center; justify-content: space-between">
        <span class="es-kpi__label">threads</span>
        <span style="color: var(--muted)">{{ template "icon-folder" }}</span>
      </div>
      <div style="font-size: var(--ds-text-lg); font-weight: 500; color: var(--ink)">{{ .NumThreads }}</div>
      <div class="es-spec">workdirs</div>
    </div>

    <div class="es-card" style="padding: 16px; display: flex; flex-direction: column; gap: 10px">
      <div style="display: flex; align-items: center; justify-content: space-between">
        <span class="es-kpi__label">free disk</span>
        <span style="color: var(--muted)">{{ template "icon-hdd" }}</span>
      </div>
      <div style="font-size: var(--ds-text-lg); font-weight: 500; color: var(--ink)">{{ .FreeDisk }}</div>
      <div class="es-spec">on data volume</div>
    </div>

    <div class="es-card" style="padding: 16px; display: flex; flex-direction: column; gap: 10px">
      <div style="display: flex; align-items: center; justify-content: space-between">
        <span class="es-kpi__label">uptime</span>
        <span style="color: var(--muted)">{{ template "icon-clock-lg" }}</span>
      </div>
      <div style="font-size: var(--ds-text-lg); font-weight: 500; color: var(--ink)">{{ .Uptime }}</div>
      <div class="es-spec">since last restart</div>
    </div>
  </div>

  <div class="es-card" style="margin-top: 18px">
    <div class="es-card__head"><span class="es-card__title">raw /healthz</span></div>
    <div style="padding: 16px">
      <div class="es-spec" style="margin-bottom: 8px">GET /healthz · {{ if .OK }}200{{ else }}503{{ end }}</div>
      <pre class="es-jsonview es-scroll">{{ .RawJSON }}</pre>
    </div>
  </div>
</div>
{{ end }}`

// Inline SVG icon templates — Lucide-style, 1.5px stroke, currentColor. Kept
// as defines so pages stay readable and SVGs aren't duplicated.
const iconsTpl = `
{{ define "icon-hash" }}<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><line x1="4" y1="9" x2="20" y2="9"/><line x1="4" y1="15" x2="20" y2="15"/><line x1="10" y1="3" x2="8" y2="21"/><line x1="16" y1="3" x2="14" y2="21"/></svg>{{ end }}
{{ define "icon-msg" }}<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>{{ end }}
{{ define "icon-plus" }}<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="M12 5v14"/></svg>{{ end }}
{{ define "icon-grip" }}<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="6" r="1"/><circle cx="9" cy="12" r="1"/><circle cx="9" cy="18" r="1"/><circle cx="15" cy="6" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="18" r="1"/></svg>{{ end }}
{{ define "icon-kebab" }}<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="5" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="12" cy="19" r="1"/></svg>{{ end }}
{{ define "icon-key-sm" }}<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="4.5"/><path d="m10.7 12.3 8.3-8.3"/><path d="m17 5 3 3"/><path d="m14 8 3 3"/></svg>{{ end }}
{{ define "icon-key" }}<svg xmlns="http://www.w3.org/2000/svg" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="4.5"/><path d="m10.7 12.3 8.3-8.3"/><path d="m17 5 3 3"/><path d="m14 8 3 3"/></svg>{{ end }}
{{ define "icon-clock-sm" }}<svg xmlns="http://www.w3.org/2000/svg" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>{{ end }}
{{ define "icon-clock-lg" }}<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>{{ end }}
{{ define "icon-lock-sm" }}<svg xmlns="http://www.w3.org/2000/svg" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>{{ end }}
{{ define "icon-lock-lg" }}<svg xmlns="http://www.w3.org/2000/svg" width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>{{ end }}
{{ define "icon-shield" }}<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1Z"/><path d="m9 12 2 2 4-4"/></svg>{{ end }}
{{ define "icon-shield-lg" }}<svg xmlns="http://www.w3.org/2000/svg" width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1Z"/><path d="m9 12 2 2 4-4"/></svg>{{ end }}
{{ define "icon-power" }}<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>{{ end }}
{{ define "icon-trash" }}<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/></svg>{{ end }}
{{ define "icon-alert" }}<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" style="color: var(--danger)"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>{{ end }}
{{ define "icon-alert-lg" }}<svg xmlns="http://www.w3.org/2000/svg" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>{{ end }}
{{ define "icon-check-lg" }}<svg xmlns="http://www.w3.org/2000/svg" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg>{{ end }}
{{ define "icon-eye" }}<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>{{ end }}
{{ define "icon-x" }}<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>{{ end }}
{{ define "icon-chev-l" }}<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" style="transform: rotate(180deg)"><path d="m9 18 6-6-6-6"/></svg>{{ end }}
{{ define "icon-folder" }}<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>{{ end }}
{{ define "icon-file" }}<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v5h5"/></svg>{{ end }}
{{ define "icon-copy" }}<svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>{{ end }}
{{ define "icon-inbox" }}<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="22 12 16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/></svg>{{ end }}
{{ define "icon-box" }}<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>{{ end }}
{{ define "icon-db" }}<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5"/><path d="M3 12a9 3 0 0 0 18 0"/></svg>{{ end }}
{{ define "icon-hdd" }}<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="12" x2="2" y2="12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><line x1="6" y1="16" x2="6.01" y2="16"/><line x1="10" y1="16" x2="10.01" y2="16"/></svg>{{ end }}
`
