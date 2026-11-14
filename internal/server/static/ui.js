/* Alcove's one script (D-29, D-36). It only tells people that something is happening. Without it
   every page still works: forms submit, uploads upload, jobs finish and the Backups page refreshes
   itself. It reads and stores nothing, never touches a password, and sends nothing but the upload
   form itself, exactly as the browser would (fields first, file last).

   Three jobs:
   1. The upload bar on My data (real progress, then "Checking the backup" with a running clock).
   2. A "Working..." bar for any form that takes a moment, and a guard against double-clicks.
   3. For a download, "Preparing your download..." until the browser has really received the file
      (the server sets a short cookie holding the token this script made for that one request). */
(function () {
  "use strict";

  var timer = null, started = 0, baseText = "", banner = null, bannerText = null, locked = [];

  function el(tag, cls) {
    var e = document.createElement(tag);
    if (cls) { e.className = cls; }
    return e;
  }

  function ensureBanner() {
    if (banner) { return; }
    banner = el("div", "busy");
    banner.setAttribute("role", "status");
    banner.setAttribute("aria-live", "polite");
    banner.hidden = true;
    var bar = el("progress");
    bar.setAttribute("aria-label", "Working");
    bannerText = el("span");
    banner.appendChild(bar);
    banner.appendChild(bannerText);
    document.body.appendChild(banner);
  }

  function clock() {
    var s = Math.max(0, Math.round((Date.now() - started) / 1000));
    bannerText.textContent = baseText + (s >= 3 ? " (" + s + " s)" : "");
  }

  function showBusy(msg) {
    ensureBanner();
    baseText = msg || "Working...";
    started = Date.now();
    banner.hidden = false;
    clock();
    if (timer) { clearInterval(timer); }
    timer = setInterval(clock, 1000);
  }

  function hideBusy() {
    if (timer) { clearInterval(timer); timer = null; }
    if (banner) { banner.hidden = true; }
    locked.forEach(function (b) { b.disabled = false; });
    locked = [];
  }

  function lock(form) {
    var bs = form.querySelectorAll("button[type=submit], button:not([type]), input[type=submit]");
    for (var i = 0; i < bs.length; i++) {
      if (!bs[i].disabled) { bs[i].disabled = true; locked.push(bs[i]); }
    }
  }

  // 1. The upload bar. It takes over only when a file is chosen.
  var upform = document.getElementById("upform");
  if (upform && window.XMLHttpRequest && window.FormData) {
    var bar = document.getElementById("bar");
    var status = document.getElementById("status");
    var go = document.getElementById("go");
    var upStart = 0, upTimer = null;
    var show = function (msg, bad) {
      status.textContent = msg;
      status.className = bad ? "notice bad" : "muted";
    };
    var stopUp = function () {
      if (upTimer) { clearInterval(upTimer); upTimer = null; }
      go.disabled = false;
      bar.hidden = true;
    };
    upform.addEventListener("submit", function (e) {
      var file = upform.elements.file;
      if (!file || !file.files || !file.files.length) { return; }
      e.preventDefault();
      var x = new XMLHttpRequest();
      x.open("POST", upform.getAttribute("action"));
      x.setRequestHeader("X-Requested-With", "alcove");
      var checking = false;
      x.upload.onprogress = function (ev) {
        if (!ev.lengthComputable) { return; }
        var pct = Math.round((ev.loaded / ev.total) * 100);
        if (pct < 100) {
          bar.value = pct;
          show("Uploading " + pct + "%");
        } else if (!checking) {
          checking = true;
          bar.removeAttribute("value"); // from here on the length is unknown: an animated bar
          var t0 = Date.now();
          upTimer = setInterval(function () {
            show("Checking the backup. This can take a minute (" + Math.round((Date.now() - t0) / 1000) + " s).");
          }, 1000);
          show("Checking the backup. This can take a minute.");
        }
      };
      x.onload = function () {
        var r;
        try { r = JSON.parse(x.responseText); } catch (err) { r = null; }
        if (r && r.ok) { window.location.href = r.redirect || "/uploads"; return; }
        stopUp();
        show((r && r.message) || "The upload failed (status " + x.status + ").", true);
      };
      x.onerror = function () {
        stopUp();
        show("The connection was lost before the upload finished. Nothing was saved. Try again.", true);
      };
      go.disabled = true;
      bar.hidden = false;
      bar.value = 0;
      upStart = Date.now();
      show("Starting the upload");
      x.send(new FormData(upform));
    });
  }

  // 2 and 3. Every other POST form.
  document.addEventListener("submit", function (e) {
    var f = e.target;
    if (e.defaultPrevented || !f || f.tagName !== "FORM" || (f.method || "").toLowerCase() !== "post") { return; }
    var msg = f.getAttribute("data-busy") || "Working...";
    var download = f.hasAttribute("data-download");
    var token = "";
    if (download) {
      token = Math.random().toString(36).slice(2, 12) + Date.now().toString(36);
      var h = document.createElement("input");
      h.type = "hidden"; h.name = "dltoken"; h.value = token;
      f.appendChild(h);
      msg = f.getAttribute("data-busy") || "Preparing your download...";
    }
    // Lock the buttons only after the browser has read the form, or a button's own name and value
    // would be lost.
    setTimeout(function () {
      lock(f);
      showBusy(msg);
      if (download) { watchDownload(token); }
    }, 0);
  });

  function watchDownload(token) {
    var tries = 0;
    var poll = setInterval(function () {
      tries++;
      if (document.cookie.indexOf("alcove_dl=" + token) !== -1 || tries > 3000) {
        clearInterval(poll);
        document.cookie = "alcove_dl=; Max-Age=0; path=/";
        hideBusy();
        if (tries <= 3000) {
          showBusy("Your download has started. Look in your downloads.");
          clearInterval(timer); timer = null;
          setTimeout(hideBusy, 5000);
        }
      }
    }, 300);
  }

  // A page restored from the back/forward cache must not still say "Working...".
  window.addEventListener("pageshow", function (ev) { if (ev.persisted) { hideBusy(); } });

  // 4. A running job (backup, verify, drill): show how long it has been going.
  var since = document.querySelectorAll("[data-since]");
  if (since.length) {
    var tick = function () {
      for (var i = 0; i < since.length; i++) {
        var s = Math.max(0, Math.round(Date.now() / 1000 - Number(since[i].getAttribute("data-since"))));
        since[i].textContent = s >= 60 ? Math.floor(s / 60) + " min " + (s % 60) + " s" : s + " s";
      }
    };
    tick();
    setInterval(tick, 1000);
  }
})();
