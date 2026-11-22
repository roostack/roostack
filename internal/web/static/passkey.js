// Passkeys (SPEC.md §5, "Passkeys"; internal/web/passkeys.go). Pages work
// without this script: what's passkey-only is hidden until it runs.
//
// A button with data-passkey="<options address>" asks Roostack for a
// challenge, has the browser ask the device, and puts the device's answer
// in the passkey field of the form named by data-form, which it submits.
// data-create makes a new passkey instead of using one. An input with
// data-passkey-autofill offers the lab's passkeys in its autofill
// (conditional mediation). Anything marked data-passkey-shown is shown
// once the browser can do passkeys.
(function () {
  "use strict";
  if (!window.PublicKeyCredential || !window.isSecureContext) return;

  function b64urlToBuf(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    const bin = atob(s), out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out.buffer;
  }
  function bufToB64url(buf) {
    const bytes = new Uint8Array(buf);
    let bin = "";
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  function ids(list) {
    return (list || []).map(function (c) { return Object.assign({}, c, { id: b64urlToBuf(c.id) }); });
  }

  // The options as the browser wants them: Roostack sends bytes as
  // base64url text.
  function creationOptions(pk) {
    if (PublicKeyCredential.parseCreationOptionsFromJSON) return PublicKeyCredential.parseCreationOptionsFromJSON(pk);
    return Object.assign({}, pk, {
      challenge: b64urlToBuf(pk.challenge),
      user: Object.assign({}, pk.user, { id: b64urlToBuf(pk.user.id) }),
      excludeCredentials: ids(pk.excludeCredentials),
    });
  }
  function requestOptions(pk) {
    if (PublicKeyCredential.parseRequestOptionsFromJSON) return PublicKeyCredential.parseRequestOptionsFromJSON(pk);
    return Object.assign({}, pk, { challenge: b64urlToBuf(pk.challenge), allowCredentials: ids(pk.allowCredentials) });
  }

  // The device's answer as JSON, as Roostack reads it.
  function answer(cred) {
    if (cred.toJSON) {
      try { return JSON.stringify(cred.toJSON()); } catch (e) { /* older browsers: below */ }
    }
    const r = cred.response, out = {
      id: cred.id, rawId: bufToB64url(cred.rawId), type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
      response: { clientDataJSON: bufToB64url(r.clientDataJSON) },
    };
    if (r.attestationObject) {
      out.response.attestationObject = bufToB64url(r.attestationObject);
      if (r.getTransports) out.response.transports = r.getTransports();
    } else {
      out.response.authenticatorData = bufToB64url(r.authenticatorData);
      out.response.signature = bufToB64url(r.signature);
      if (r.userHandle) out.response.userHandle = bufToB64url(r.userHandle);
    }
    return JSON.stringify(out);
  }

  async function options(url) {
    const res = await fetch(url, { method: "POST", credentials: "same-origin", headers: { Accept: "application/json" } });
    const body = await res.json();
    if (!res.ok) throw new Error(body.error || "Roostack couldn't start the passkey.");
    return body;
  }

  function submit(formID, json) {
    const form = document.getElementById(formID);
    let field = form.querySelector('input[name="passkey"]');
    if (!field) {
      field = document.createElement("input");
      field.type = "hidden";
      field.name = "passkey";
      form.appendChild(field);
    }
    field.value = json;
    // Fields the passkey's form shares with the page's own (keep me
    // signed in, a name): data-copy="<field name> <element id>".
    form.querySelectorAll("[data-copy]").forEach(function (el) {
      const src = document.getElementById(el.dataset.copy);
      if (src) el.value = src.type === "checkbox" ? (src.checked ? "on" : "") : src.value;
    });
    form.submit();
  }

  function say(el, msg) {
    const box = el.closest("[data-passkey-area]") || document;
    const p = box.querySelector("[data-passkey-error]");
    if (p) { p.textContent = msg; p.hidden = false; }
  }

  let autofill = null; // the conditional request, cancelled by a button

  async function run(button) {
    if (autofill) { autofill.abort(); autofill = null; }
    button.disabled = true;
    try {
      const opts = await options(button.dataset.passkey);
      const cred = button.hasAttribute("data-create")
        ? await navigator.credentials.create({ publicKey: creationOptions(opts.publicKey) })
        : await navigator.credentials.get({ publicKey: requestOptions(opts.publicKey) });
      submit(button.dataset.form, answer(cred));
    } catch (e) {
      button.disabled = false;
      say(button, e.name === "NotAllowedError" || e.name === "AbortError"
        ? (button.dataset.failed || "The passkey didn't work, or you cancelled it.")
        : e.name === "InvalidStateError" ? "This device already has a passkey for your account."
        : e.message);
    }
  }

  async function offerInAutofill(input) {
    if (!PublicKeyCredential.isConditionalMediationAvailable || !(await PublicKeyCredential.isConditionalMediationAvailable())) return;
    try {
      const opts = await options(input.dataset.passkeyAutofill);
      autofill = new AbortController();
      const cred = await navigator.credentials.get({ publicKey: requestOptions(opts.publicKey), mediation: "conditional", signal: autofill.signal });
      submit(input.dataset.form, answer(cred));
    } catch (e) {
      // Cancelled for a button, or the browser has nothing to offer: the
      // password still works.
    }
  }

  document.querySelectorAll("[data-passkey-shown]").forEach(function (el) { el.hidden = false; });
  document.querySelectorAll("button[data-passkey]").forEach(function (b) {
    b.addEventListener("click", function () { run(b); });
  });
  const input = document.querySelector("input[data-passkey-autofill]");
  if (input) offerInAutofill(input);
})();
