// One module for every page state, so each part checks that its own markup is
// present before wiring anything: the landing page has no client picker, and
// only the page for an existing token carries the reissue confirmation.

function reissueConfirmation() {
  const ask = document.getElementById("ask");
  const confirmBox = document.getElementById("confirm");
  const cancel = document.getElementById("cancel");
  if (!ask || !confirmBox || !cancel) return;

  ask.addEventListener("click", () => { confirmBox.hidden = false; ask.hidden = true; });
  cancel.addEventListener("click", () => {
    confirmBox.hidden = true;
    ask.hidden = false;
  });
  
}

function clientPicker() {
  const clients = document.querySelector(".clients");
  if (!clients) return;
  let token = clients.dataset.token;
  let tokenCopied = false;
  const tabs = [...document.querySelectorAll(".tab")];
  const panels = [...document.querySelectorAll(".panel")];
  	const modeButtons = [...document.querySelectorAll(".mode[data-mode]")];
  	const variants = [...document.querySelectorAll(".variant")];

  function show(id) {
    const known = tabs.some((t) => t.dataset.format === id);
    if (!known) return;
    tabs.forEach((t) => {
      const on = t.dataset.format === id;
      t.classList.toggle("on", on);
      t.setAttribute("aria-selected", on);
    });
    panels.forEach((p) => { p.hidden = p.dataset.format !== id; });
    // The wrapper carries the client too, which is what colours the block's
    // rail: the palette keys off data-format wherever it sits.
    clients.dataset.format = id;
    try { localStorage.setItem("mcp-client", id); } catch {}
  }

  tabs.forEach((t) => t.addEventListener("click", () => show(t.dataset.format)));

  	function showMode(id) {
  	  if (!modeButtons.some((button) => button.dataset.mode === id)) return;
  	  modeButtons.forEach((button) => {
  	    const on = button.dataset.mode === id;
  	    button.classList.toggle("on", on);
  	    button.setAttribute("aria-checked", on);
  	  });
  	  variants.forEach((variant) => {
  	    variant.hidden = variant.dataset.mode !== id;
  	    variant.setAttribute("aria-hidden", variant.hidden);
  	  });
  	  try { localStorage.setItem("mcp-launch-mode", id); } catch {}
  	}

  	modeButtons.forEach((button) => button.addEventListener("click", () => showMode(button.dataset.mode)));

  // Remembering the choice is a convenience; a browser that refuses storage
  // just starts on the first tab.
  try {
    const saved = localStorage.getItem("mcp-client");
    if (saved) show(saved);
  	  const savedMode = localStorage.getItem("mcp-launch-mode");
  	  if (savedMode) showMode(savedMode);
  } catch {}

  let storage = "inline";
  document.querySelectorAll(".storage-mode").forEach((button) => {
    button.addEventListener("click", () => {
      storage = button.dataset.storage;
      document.querySelectorAll(".storage-mode").forEach((item) => {
        const on = item.dataset.storage === storage;
        item.classList.toggle("on", on);
        item.setAttribute("aria-checked", on);
      });
      document.querySelectorAll(".storage").forEach((pre) => { pre.hidden = pre.dataset.storage !== storage; });
      document.querySelector(".env-guide").hidden = storage !== "env";
      document.querySelectorAll(".copy").forEach((copy) => {
        const pre = copy.closest(".snippet").querySelector("pre:not([hidden])");
        copy.disabled = tokenCopied && !!pre.querySelector(".tok");
      });
    });
  });

  document.querySelectorAll(".copy").forEach((copy) => {

    const block = copy.closest(".snippet");
    const label = copy.querySelector(".label");
    const originalLabel = label.textContent;

    copy.addEventListener("click", async () => {
      const snippet = block.querySelector("pre:not([hidden])");
      const masked = snippet.querySelector(".tok");
      // A function replacement keeps $-sequences in the token literal.
      const real = token && masked
        ? snippet.textContent.replace(masked.textContent, () => token)
        : snippet.textContent;
      try {
        await navigator.clipboard.writeText(real);
        label.textContent = "Copied";
        copy.classList.add("ok");
        if (token && masked) {
          tokenCopied = true;
          token = "";
          delete clients.dataset.token;
          document.querySelectorAll(".tok").forEach((node) => { node.textContent = "[token copied]"; });
          document.querySelectorAll(".copy").forEach((button) => {
            if (button.closest(".snippet").querySelector("pre:not([hidden]) .tok")) button.disabled = true;
          });
        }
      } catch {
        // Clipboard access can be refused. Unmask first, or a hand-made
        // selection copies the asterisks.
        if (token && masked) masked.textContent = token;
        getSelection().selectAllChildren(snippet);
        label.textContent = "Press Ctrl/Cmd+C";
      }
      setTimeout(() => { label.textContent = originalLabel; copy.classList.remove("ok"); }, 2000);
    });
  });
  

}

reissueConfirmation();
clientPicker();
