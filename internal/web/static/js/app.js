/* Espur admin client behaviors.
   Server-rendered HTML; this file adds the interactive bits the design needs:
   theme toggle, drawer/modal open-close, kebab menus, copy buttons,
   cooldown countdowns, drag-reorder, inline set-key panel, toast helper.
   No build step; talks to plain form-POST endpoints + redirects. */
(function () {
  "use strict";

  /* ---------------- theme ---------------- */
  function applyTheme(t) {
    document.documentElement.setAttribute("data-theme", t);
    try { localStorage.setItem("espur-theme", t); } catch (e) {}
    const btn = document.getElementById("theme-toggle");
    if (btn) btn.innerHTML = t === "light" ? moonSvg() : sunSvg();
  }
  function initTheme() {
    let t = "light";
    try { t = localStorage.getItem("espur-theme") || "light"; } catch (e) {}
    applyTheme(t);
    const btn = document.getElementById("theme-toggle");
    if (btn) {
      btn.addEventListener("click", function () {
        const cur = document.documentElement.getAttribute("data-theme") || "light";
        applyTheme(cur === "light" ? "dark" : "light");
      });
    }
  }
  function sunSvg() {
    return '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/></svg>';
  }
  function moonSvg() {
    return '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/></svg>';
  }

  /* ---------------- toasts ---------------- */
  function ensureToastHost() {
    let host = document.querySelector(".es-toasts");
    if (!host) {
      host = document.createElement("div");
      host.className = "es-toasts";
      document.body.appendChild(host);
    }
    return host;
  }
  function toast(kind, title, msg) {
    const host = ensureToastHost();
    const div = document.createElement("div");
    div.className = "es-toast es-toast--" + kind;
    div.innerHTML =
      '<span class="es-toast__icon">' + (kind === "err" ? alertSvg() : kind === "info" ? infoSvg() : checkSvg()) + "</span>" +
      '<div class="es-toast__body">' +
        '<div class="es-toast__title"></div>' +
        (msg ? '<div class="es-toast__msg"></div>' : "") +
      "</div>" +
      '<button class="es-toast__x" aria-label="dismiss">' + xSvg() + "</button>";
    div.querySelector(".es-toast__title").textContent = title;
    if (msg) div.querySelector(".es-toast__msg").textContent = msg;
    div.querySelector(".es-toast__x").addEventListener("click", function () { div.remove(); });
    host.appendChild(div);
    if (kind !== "err") setTimeout(function () { div.remove(); }, 4200);
  }
  function checkSvg() { return '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg>'; }
  function alertSvg() { return '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>'; }
  function infoSvg() { return '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>'; }
  function xSvg() { return '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>'; }

  function consumeFlash() {
    const m = document.cookie.match(/(?:^|; )espur_flash=([^;]+)/);
    if (!m) return;
    document.cookie = "espur_flash=; Max-Age=0; Path=/";
    try {
      const payload = JSON.parse(decodeURIComponent(m[1]));
      if (payload && payload.title) toast(payload.kind || "ok", payload.title, payload.msg || "");
    } catch (e) {}
  }

  /* ---------------- copy buttons ---------------- */
  function initCopy() {
    document.querySelectorAll("[data-copy]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const val = btn.getAttribute("data-copy");
        try { navigator.clipboard.writeText(val); } catch (e) {}
        const orig = btn.innerHTML;
        btn.innerHTML = checkSvg();
        setTimeout(function () { btn.innerHTML = orig; }, 1100);
      });
    });
  }

  /* ---------------- kebab menus ---------------- */
  function initMenus() {
    document.querySelectorAll(".es-menu-wrap").forEach(function (wrap) {
      const btn = wrap.querySelector("[data-menu-toggle]");
      const menu = wrap.querySelector(".es-menu");
      if (!btn || !menu) return;
      btn.addEventListener("click", function (e) {
        e.stopPropagation();
        document.querySelectorAll(".es-menu").forEach(function (m) { if (m !== menu) m.style.display = "none"; });
        menu.style.display = menu.style.display === "block" ? "none" : "block";
      });
      document.addEventListener("click", function (e) {
        if (!wrap.contains(e.target)) menu.style.display = "none";
      });
    });
  }

  /* ---------------- drawer + modal ---------------- */
  function showOverlay(id) {
    const el = document.getElementById(id);
    if (!el) return;
    el.removeAttribute("hidden");
    document.body.style.overflow = "hidden";
  }
  function hideOverlay(id) {
    const el = document.getElementById(id);
    if (!el) return;
    el.setAttribute("hidden", "");
    document.body.style.overflow = "";
  }
  function initOverlays() {
    document.querySelectorAll("[data-open]").forEach(function (btn) {
      btn.addEventListener("click", function () { showOverlay(btn.getAttribute("data-open")); });
    });
    document.querySelectorAll("[data-close]").forEach(function (btn) {
      btn.addEventListener("click", function () { hideOverlay(btn.getAttribute("data-close")); });
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        document.querySelectorAll(".es-overlay:not([hidden])").forEach(function (el) {
          el.setAttribute("hidden", "");
        });
        document.body.style.overflow = "";
      }
    });
  }

  /* ---------------- secret field show/hide ---------------- */
  function initSecretToggles() {
    document.querySelectorAll("[data-secret-toggle]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const target = document.getElementById(btn.getAttribute("data-secret-toggle"));
        if (!target) return;
        target.type = target.type === "password" ? "text" : "password";
      });
    });
  }

  /* ---------------- add-vendor drawer (model dropdown + cred radio) ---------------- */
  function initAddVendorDrawer() {
    const sel = document.getElementById("model-select");
    if (!sel) return;
    const envField = document.getElementById("env-field");
    const credByo = document.getElementById("cred-byo");
    const credOauth = document.getElementById("cred-oauth");
    const credOauthOpt = document.getElementById("cred-oauth-opt");
    const oauthDesc = document.getElementById("cred-oauth-desc");
    const vidInput = document.getElementById("vendor-id-input");
    const credHint = document.getElementById("cred-hint");
    let vidTouched = false;
    if (vidInput) vidInput.addEventListener("input", function () { vidTouched = true; });

    function setCredOpt(opt, on) {
      if (!opt) return;
      opt.setAttribute("data-sel", on ? "true" : "false");
    }
    function chooseCred(kind) {
      if (kind === "oauth" && credOauthOpt && credOauthOpt.getAttribute("data-disabled") === "true") return;
      if (credByo) credByo.checked = kind === "byo_key";
      if (credOauth) credOauth.checked = kind === "oauth";
      setCredOpt(document.getElementById("cred-byo-opt"), kind === "byo_key");
      setCredOpt(credOauthOpt, kind === "oauth");
      if (credHint) {
        credHint.textContent = kind === "oauth"
          ? "OAuth vendors resolve their token from the provider session — no env var is read."
          : "The key you set is written to this variable at invocation time.";
      }
    }
    document.querySelectorAll("[data-cred-opt]").forEach(function (opt) {
      opt.addEventListener("click", function () { chooseCred(opt.getAttribute("data-cred-opt")); });
    });

    function sync() {
      const opt = sel.options[sel.selectedIndex];
      if (!opt) return;
      const env = opt.getAttribute("data-env") || "";
      const oauthOk = opt.getAttribute("data-oauth") === "1";
      const provName = opt.getAttribute("data-provider-name") || "";
      if (envField) envField.value = env || "(OAuth-only — no env var)";
      const provHint = document.getElementById("provider-hint");
      if (provHint) provHint.textContent = provName;
      if (credOauthOpt) {
        credOauthOpt.setAttribute("data-disabled", oauthOk ? "false" : "true");
        if (oauthDesc) oauthDesc.textContent = oauthOk ? "via opencode session" : "not supported";
      }
      const byoOpt = document.getElementById("cred-byo-opt");
      if (byoOpt) byoOpt.setAttribute("data-disabled", env ? "false" : "true");
      if (!env) chooseCred("oauth");
      else if (!oauthOk && credOauth && credOauth.checked) chooseCred("byo_key");
      // suggested vendor id
      if (!vidTouched && vidInput) {
        const model = opt.value.split("/").slice(1).join("/");
        vidInput.value = model.replace(/[^a-z0-9]+/gi, "-").toLowerCase().replace(/^-|-$/g, "") + "-vendor";
      }
    }
    sel.addEventListener("change", sync);
    sync();
    chooseCred(credByo && credByo.checked ? "byo_key" : "oauth");
  }

  /* ---------------- inline set-key panel ---------------- */
  function initSetKeyPanels() {
    document.querySelectorAll("[data-setkey-toggle]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const id = btn.getAttribute("data-setkey-toggle");
        const panel = document.getElementById("setkey-" + id);
        if (!panel) return;
        document.querySelectorAll(".es-inline").forEach(function (p) { if (p !== panel) p.setAttribute("hidden", ""); });
        if (panel.hasAttribute("hidden")) {
          panel.removeAttribute("hidden");
          const inp = panel.querySelector("input[type=password],input[type=text]");
          if (inp) inp.focus();
        } else {
          panel.setAttribute("hidden", "");
        }
      });
    });
  }

  /* ---------------- cooldown countdown ---------------- */
  function tickCountdowns() {
    document.querySelectorAll("[data-cooldown-until]").forEach(function (el) {
      const until = parseInt(el.getAttribute("data-cooldown-until"), 10) * 1000;
      const left = Math.max(0, until - Date.now());
      if (left <= 0) {
        el.innerHTML = '<span class="es-status__dot"></span>eligible';
        el.className = "es-status es-status--ok";
        el.removeAttribute("data-cooldown-until");
        return;
      }
      const s = Math.floor(left / 1000);
      const m = Math.floor(s / 60);
      const ss = s % 60;
      const span = el.querySelector(".es-countdown-val");
      if (span) span.textContent = m + ":" + (ss < 10 ? "0" + ss : ss);
    });
  }
  function initCountdowns() {
    tickCountdowns();
    setInterval(tickCountdowns, 1000);
  }

  /* ---------------- drag reorder ---------------- */
  function initReorder() {
    const tbody = document.getElementById("vendor-rows");
    if (!tbody) return;
    const form = document.getElementById("reorder-form");
    let dragRow = null;

    tbody.querySelectorAll(".es-trow[data-vid]").forEach(function (row) {
      row.setAttribute("draggable", "true");
      row.addEventListener("dragstart", function (e) {
        dragRow = row;
        row.classList.add("es-trow--dragging");
        try { e.dataTransfer.setData("text/plain", row.getAttribute("data-vid")); } catch (err) {}
        e.dataTransfer.effectAllowed = "move";
      });
      row.addEventListener("dragend", function () {
        if (dragRow) dragRow.classList.remove("es-trow--dragging");
        tbody.querySelectorAll(".es-trow--over").forEach(function (r) { r.classList.remove("es-trow--over"); });
        dragRow = null;
      });
      row.addEventListener("dragover", function (e) {
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";
        tbody.querySelectorAll(".es-trow--over").forEach(function (r) { r.classList.remove("es-trow--over"); });
        row.classList.add("es-trow--over");
      });
      row.addEventListener("drop", function (e) {
        e.preventDefault();
        if (!dragRow || dragRow === row) return;
        const dragRect = dragRow.getBoundingClientRect();
        const tgtRect = row.getBoundingClientRect();
        if (dragRect.top < tgtRect.top) row.after(dragRow);
        else row.before(dragRow);
        renumber();
        submitOrder();
      });
    });

    function renumber() {
      let i = 1;
      tbody.querySelectorAll(".es-trow[data-vid] .es-pri").forEach(function (n) { n.textContent = i++; });
    }
    function submitOrder() {
      if (!form) return;
      // remove existing hidden inputs
      form.querySelectorAll('input[name="ids"]').forEach(function (i) { i.remove(); });
      tbody.querySelectorAll(".es-trow[data-vid]").forEach(function (r) {
        const inp = document.createElement("input");
        inp.type = "hidden";
        inp.name = "ids";
        inp.value = r.getAttribute("data-vid");
        form.appendChild(inp);
      });
      form.submit();
    }
  }

  /* ---------------- thread detail: tab switching ---------------- */
  function initTabs() {
    document.querySelectorAll("[data-tab]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const group = btn.closest("[data-tab-group]");
        if (!group) return;
        const tab = btn.getAttribute("data-tab");
        group.querySelectorAll("[data-tab]").forEach(function (b) {
          b.setAttribute("data-active", b === btn ? "true" : "false");
        });
        document.querySelectorAll('[data-tab-pane][data-pane-group="' + group.getAttribute("data-tab-group") + '"]').forEach(function (pane) {
          if (pane.getAttribute("data-tab-pane") === tab) pane.removeAttribute("hidden");
          else pane.setAttribute("hidden", "");
        });
      });
    });
  }

  /* ---------------- thread detail: memory file picker ---------------- */
  function initFilePicker() {
    document.querySelectorAll("[data-file-pick]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const group = btn.closest("[data-picker]");
        if (!group) return;
        group.querySelectorAll("[data-file-pick]").forEach(function (b) { b.setAttribute("aria-current", "false"); });
        btn.setAttribute("aria-current", "true");
        const file = btn.getAttribute("data-file-pick");
        const pane = document.querySelector('[data-file-view="' + group.getAttribute("data-picker") + '"]');
        if (!pane) return;
        const tpl = document.querySelector('[data-file-body="' + group.getAttribute("data-picker") + '-' + cssEscape(file) + '"]');
        pane.innerHTML = tpl ? tpl.innerHTML : '<div class="es-empty__sub">file not loaded</div>';
      });
    });
  }
  function cssEscape(s) { return s.replace(/[^a-zA-Z0-9_-]/g, function (c) { return "_" + c.charCodeAt(0); }); }

  /* ---------------- boot ---------------- */
  document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    initCopy();
    initMenus();
    initOverlays();
    initSecretToggles();
    initAddVendorDrawer();
    initSetKeyPanels();
    initCountdowns();
    initReorder();
    initTabs();
    initFilePicker();
    consumeFlash();
  });

  window.espurToast = toast;
})();
