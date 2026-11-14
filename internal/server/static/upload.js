/* Upload progress for the My data page (D-29). The only script Alcove ships. Without it the form
   still uploads, just with no progress bar. It sends the form exactly as the browser would
   (fields first, file last), keeps nothing, and never reads or stores the password. */
(function () {
  "use strict";
  var form = document.getElementById("upform");
  if (!form || !window.XMLHttpRequest || !window.FormData) { return; }
  var bar = document.getElementById("bar");
  var status = document.getElementById("status");
  var go = document.getElementById("go");

  function show(msg, bad) {
    status.textContent = msg;
    status.className = bad ? "notice bad" : "muted";
  }
  function done() { go.disabled = false; bar.hidden = true; }

  form.addEventListener("submit", function (e) {
    var file = form.elements.file;
    if (!file || !file.files || !file.files.length) { return; }
    e.preventDefault();
    var x = new XMLHttpRequest();
    x.open("POST", form.getAttribute("action"));
    x.setRequestHeader("X-Requested-With", "alcove");
    x.upload.onprogress = function (ev) {
      if (!ev.lengthComputable) { return; }
      var pct = Math.round((ev.loaded / ev.total) * 100);
      bar.value = pct;
      show(pct < 100 ? "Uploading " + pct + "%" : "Checking the backup. This can take a minute.");
    };
    x.onload = function () {
      var r;
      try { r = JSON.parse(x.responseText); } catch (err) { r = null; }
      if (r && r.ok) { window.location.href = r.redirect || "/uploads"; return; }
      done();
      show((r && r.message) || "The upload failed (status " + x.status + ").", true);
    };
    x.onerror = function () {
      done();
      show("The connection was lost before the upload finished. Nothing was saved. Try again.", true);
    };
    go.disabled = true;
    bar.hidden = false;
    bar.value = 0;
    show("Starting the upload");
    x.send(new FormData(form));
  });
})();
