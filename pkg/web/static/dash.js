(function () {
  var selected = 0;
  var palette = null;
  var paletteInput = null;
  var paletteList = null;
  var paletteItems = [];

  function cards() {
    return Array.prototype.slice.call(document.querySelectorAll(".bead-card"));
  }

  function titleOf(card) {
    var title = card.querySelector(".bead-card__title");
    var id = card.getAttribute("data-id") || "";
    return (id + " " + (title ? title.textContent : "")).trim();
  }

  function score(text, query) {
    text = text.toLowerCase();
    query = query.toLowerCase().trim();
    if (!query) return 1;
    var pos = -1;
    var total = 0;
    for (var i = 0; i < query.length; i++) {
      pos = text.indexOf(query[i], pos + 1);
      if (pos < 0) return 0;
      total += pos;
    }
    return 1000 - total;
  }

  function setSelected(next) {
    var all = cards();
    if (!all.length) return;
    selected = Math.max(0, Math.min(next, all.length - 1));
    all.forEach(function (card, index) {
      card.classList.toggle("is-selected", index === selected);
      if (index === selected) card.setAttribute("tabindex", "0");
      else card.removeAttribute("tabindex");
    });
    all[selected].scrollIntoView({ block: "nearest" });
  }

  function selectedCard() {
    var all = cards();
    if (!all.length) return null;
    if (selected >= all.length) selected = all.length - 1;
    return all[selected];
  }

  function openDetail(id) {
    if (!id) return;
    var detail = document.getElementById("detail");
    if (!detail) return;
    detail.classList.add("is-open");
    window.location.hash = id;
    if (window.htmx) {
      window.htmx.ajax("GET", "/fragments/detail/" + encodeURIComponent(id), {
        target: "#detail",
        swap: "innerHTML",
      });
      return;
    }
    fetch("/fragments/detail/" + encodeURIComponent(id))
      .then(function (res) { return res.ok ? res.text() : ""; })
      .then(function (html) { if (html) detail.innerHTML = html; });
  }

  function closeDetail() {
    var detail = document.getElementById("detail");
    if (!detail) return;
    detail.classList.remove("is-open");
    detail.innerHTML = "";
    if (window.location.hash) history.replaceState(null, "", window.location.pathname + window.location.search);
  }

  function ensurePalette() {
    if (palette) return;
    palette = document.createElement("div");
    palette.className = "dash-palette";
    palette.hidden = true;
    palette.innerHTML =
      '<div class="dash-palette__panel">' +
      '<input class="dash-palette__input" type="search" placeholder="Type a command..." autocomplete="off">' +
      '<div class="dash-palette__list"></div>' +
      "</div>";
    document.body.appendChild(palette);
    paletteInput = palette.querySelector(".dash-palette__input");
    paletteList = palette.querySelector(".dash-palette__list");
    palette.addEventListener("mousedown", function (event) {
      if (event.target === palette) closePalette();
    });
    paletteInput.addEventListener("input", renderPalette);
  }

  function renderPalette() {
    var query = paletteInput.value;
    paletteItems = cards()
      .map(function (card) { return { card: card, label: titleOf(card), score: score(titleOf(card), query) }; })
      .filter(function (item) { return item.score > 0; })
      .sort(function (a, b) { return b.score - a.score; })
      .slice(0, 12);
    paletteList.innerHTML = "";
    paletteItems.forEach(function (item, index) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "dash-palette__item" + (index === 0 ? " is-selected" : "");
      button.textContent = item.label;
      button.addEventListener("click", function () {
        closePalette();
        openDetail(item.card.getAttribute("data-id"));
      });
      paletteList.appendChild(button);
    });
  }

  function openPalette() {
    ensurePalette();
    palette.hidden = false;
    paletteInput.value = "";
    renderPalette();
    paletteInput.focus();
  }

  function closePalette() {
    if (!palette || palette.hidden) return false;
    palette.hidden = true;
    return true;
  }

  function paletteMove(delta) {
    if (!palette || palette.hidden || !paletteItems.length) return false;
    var current = paletteList.querySelector(".is-selected");
    var index = Array.prototype.indexOf.call(paletteList.children, current);
    index = Math.max(0, Math.min(index + delta, paletteItems.length - 1));
    Array.prototype.forEach.call(paletteList.children, function (item, i) {
      item.classList.toggle("is-selected", i === index);
    });
    return true;
  }

  function paletteEnter() {
    if (!palette || palette.hidden || !paletteItems.length) return false;
    var current = paletteList.querySelector(".is-selected") || paletteList.children[0];
    if (current) current.click();
    return true;
  }

  function handleKeydown(event) {
    var target = event.target;
    var typing = target && /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName);
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      openPalette();
      return;
    }
    if (event.key === "Escape") {
      if (closePalette()) return;
      closeDetail();
      return;
    }
    if (event.key === "/" && !typing) {
      event.preventDefault();
      openPalette();
      return;
    }
    if (event.key === "j" && !typing) {
      event.preventDefault();
      if (!paletteMove(1)) setSelected(selected + 1);
      return;
    }
    if (event.key === "k" && !typing) {
      event.preventDefault();
      if (!paletteMove(-1)) setSelected(selected - 1);
      return;
    }
    if (event.key === "Enter" && !typing) {
      event.preventDefault();
      if (!paletteEnter()) {
        var card = selectedCard();
        if (card) openDetail(card.getAttribute("data-id"));
      }
    }
  }

  function boot() {
    setSelected(0);
    document.body.addEventListener("click", function (event) {
      var card = event.target.closest && event.target.closest(".bead-card");
      if (card) openDetail(card.getAttribute("data-id"));
    });
    document.body.addEventListener("htmx:afterSwap", function (event) {
      if (event.detail && event.detail.target && event.detail.target.id === "parade") setSelected(0);
      if (event.detail && event.detail.target && event.detail.target.id === "detail") event.detail.target.classList.add("is-open");
    });
    document.addEventListener("keydown", handleKeydown);
    if (window.location.hash.length > 1) openDetail(window.location.hash.slice(1));
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
