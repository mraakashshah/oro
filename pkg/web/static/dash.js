(function () {
  var selected = 0;
  var palette;
  var paletteInput;
  var paletteList;
  var paletteItems = [];
  function allCards() {
    return Array.prototype.slice.call(document.querySelectorAll(".bead-card"));
  }
  function titleOf(card) {
    var title = card.querySelector(".bead-card__title");
    return ((card.getAttribute("data-id") || "") + " " + (title ? title.textContent : "")).trim();
  }
  function fuzzyScore(text, query) {
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
  function visibleCards() {
    return allCards().filter(function (card) {
      return !card.classList.contains("is-hidden");
    });
  }
  function setSelected(next) {
    var cards = visibleCards();
    if (!cards.length) return;
    selected = Math.max(0, Math.min(next, cards.length - 1));
    allCards().forEach(function (card) {
      card.classList.remove("is-selected");
      card.removeAttribute("tabindex");
    });
    cards[selected].classList.add("is-selected");
    cards[selected].setAttribute("tabindex", "0");
    cards[selected].scrollIntoView({ block: "nearest" });
  }
  function detailPath(id) {
    return "/fragments/detail/" + encodeURIComponent(id);
  }
  function openDetail(id) {
    var detail = document.getElementById("detail");
    if (!id || !detail) return;
    detail.classList.add("is-open");
    if (window.location.hash !== "#" + id) window.location.hash = id;
    if (window.htmx) {
      window.htmx.ajax("GET", detailPath(id), { target: "#detail", swap: "innerHTML" });
      return;
    }
    fetch(detailPath(id))
      .then(function (res) { return res.ok ? res.text() : ""; })
      .then(function (html) { if (html) detail.innerHTML = html; });
  }
  function closeDetail() {
    var detail = document.getElementById("detail");
    if (!detail || !detail.classList.contains("is-open")) return false;
    detail.classList.remove("is-open");
    detail.innerHTML = "";
    if (window.location.hash) history.replaceState(null, "", window.location.pathname + window.location.search);
    return true;
  }
  function applySearch() {
    var search = document.querySelector("[data-dashboard-search]");
    var query = search ? search.value : "";
    allCards().forEach(function (card) {
      card.classList.toggle("is-hidden", fuzzyScore(titleOf(card), query) === 0);
    });
    setSelected(0);
  }
  function closePalette() {
    if (!palette || palette.hidden) return false;
    palette.hidden = true;
    return true;
  }
  function ensurePalette() {
    if (palette) return;
    palette = document.createElement("div");
    palette.className = "dash-palette";
    palette.hidden = true;
    palette.innerHTML = '<div class="dash-palette__panel"><input class="dash-palette__input" type="search" placeholder="Type a command..." autocomplete="off"><div class="dash-palette__list"></div></div>';
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
    paletteItems = allCards()
      .map(function (card) {
        return { card: card, label: titleOf(card), score: fuzzyScore(titleOf(card), query) };
      })
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
  function moveSelection(delta) {
    setSelected(selected + delta);
  }
  function openSelected() {
    var card = visibleCards()[selected] || visibleCards()[0];
    if (card) openDetail(card.getAttribute("data-id"));
  }
  function handleKeydown(event) {
    var target = event.target;
    var typing = target && /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName);
    var key = event.key.toLowerCase();
    if ((event.metaKey || event.ctrlKey) && key === "k") {
      event.preventDefault();
      openPalette();
    } else if (event.key === "Escape") {
      if (!closePalette()) closeDetail();
    } else if (event.key === "/" && !typing) {
      var search = document.querySelector("[data-dashboard-search]");
      if (search) {
        event.preventDefault();
        search.focus();
      }
    } else if (key === "j" && !typing) {
      event.preventDefault();
      if (!paletteMove(1)) moveSelection(1);
    } else if (key === "k" && !typing) {
      event.preventDefault();
      if (!paletteMove(-1)) moveSelection(-1);
    } else if (event.key === "Enter" && !typing) {
      event.preventDefault();
      if (!paletteEnter()) openSelected();
    }
  }
  function boot() {
    var search = document.querySelector("[data-dashboard-search]");
    if (search) search.addEventListener("input", applySearch);
    setSelected(0);
    document.body.addEventListener("click", function (event) {
      var card = event.target.closest && event.target.closest(".bead-card");
      if (card) openDetail(card.getAttribute("data-id"));
    });
    document.body.addEventListener("htmx:afterSwap", function (event) {
      var target = event.detail && event.detail.target;
      if (target && target.id === "parade") applySearch();
      if (target && target.id === "detail") target.classList.add("is-open");
    });
    document.addEventListener("keydown", handleKeydown);
    if (window.location.hash.length > 1) openDetail(window.location.hash.slice(1));
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
