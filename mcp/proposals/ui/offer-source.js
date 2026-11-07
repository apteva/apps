import {proposalLocale} from "./locale";

const root = document.querySelector("#app");
const token = location.hash.slice(1);
const project = new URLSearchParams(location.search).get("project_id") || "";
let text = proposalLocale();

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text) node.textContent = text;
  if (className) node.className = className;
  return node;
}

async function request(path, body) {
  const endpoint = new URL(path, location.href);
  endpoint.searchParams.set("project_id", project);
  const res = await fetch(endpoint, {
    method: "POST", credentials: "same-origin",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({token, ...body}),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(text.lang === "fr" ? text.requestError : data.error || text.requestError);
  return data;
}

function render(view) {
  text = proposalLocale(view.language || view.content?.language);
  document.documentElement.lang = text.lang;
  document.title = text.proposal;
  root.replaceChildren();
  if (!view.content) {
    root.append(element("h1", text.unavailable), element("p", text.closed[view.state] || text.closedDefault));
    return;
  }
  const {content, response} = view;
  const decisions = text.decisions;
  const choices = {...response?.selection};
  const fmt = new Intl.NumberFormat(text.locale, {style: "currency", currency: content.currency});
  const money = (minor) => fmt.format(minor / 100);
  const date = new Intl.DateTimeFormat(text.locale, {dateStyle: "long"});
  const header = element("header", "", "hero");
  header.append(
    element("p", content.sender + " · " + text.proposal, "eyebrow"),
    element("h1", content.title),
    element("p", [content.client_name && text.prepared + " " + content.client_name, text.revision + " " + view.number].filter(Boolean).join(" · "), "muted"),
  );
  if (content.overview) header.append(element("p", content.overview, "copy"));
  root.append(header);

  const layout = element("div", "", "layout");
  const scope = element("section", "", "stack");
  const summary = element("aside", "", "card stack");
  summary.id = "proposal-summary";
  layout.append(scope, summary);
  root.append(layout);

  const total = element("p", "", "total");
  const mobileTotal = element("output");
  if (!response) {
    const mobileSummary = element("div", "", "mobile-summary");
    const jump = element("button", text.review);
    jump.type = "button";
    jump.addEventListener("click", () => {
      summary.scrollIntoView({block: "start"});
      summary.focus({preventScroll: true});
    });
    summary.tabIndex = -1;
    mobileSummary.append(mobileTotal, jump);
    root.append(mobileSummary);
  }
  total.setAttribute("aria-live", "polite");
  const refreshTotal = () => {
    let amount = 0;
    content.categories.forEach((group, i) => group.items.forEach((item, j) => {
      if (item.required || choices[i + ":" + j] === "include") amount += item.amount_minor;
    }));
    total.textContent = money(response?.total_minor ?? amount);
    mobileTotal.textContent = money(amount);
  };

  content.categories.forEach((group, i) => {
    const section = element("section", "", "card category");
    const rule = text.rules;
    const groupHead = element("div", "", "category-head");
    const groupTitle = element("div", "", "stack-tight");
    groupTitle.append(element("h2", group.name));
    const optional = group.items.filter((item) => !item.required);
    if (optional.length) groupTitle.append(element("p", rule[group.rule], "muted"));
    const groupStatus = element("p", "", "group-status");
    groupStatus.setAttribute("aria-live", "polite");
    groupHead.append(groupTitle, groupStatus);
    section.append(groupHead);
    const controls = [];
    const groupButtons = [];
    const refreshGroup = () => {
      let count = 0, amount = 0;
      group.items.forEach((item, j) => {
        if (item.required || choices[i + ":" + j] === "include") {
          count++;
          amount += item.amount_minor;
        }
      });
      groupStatus.textContent = text.groupCount(count, group.items.length) + " · " + money(amount);
      for (const {key, row, buttons} of controls) {
        const value = choices[key] || "unselected";
        row.dataset.choice = value;
        for (const button of buttons) button.setAttribute("aria-pressed", String(button.value === value));
      }
      for (const button of groupButtons) {
        const all = group.items.every((item, j) => item.required || choices[i + ":" + j] === button.value);
        button.setAttribute("aria-pressed", String(all));
      }
      refreshTotal();
    };
    if (!response && optional.length && group.rule === "any") {
      const actions = element("div", "", "group-actions");
      actions.setAttribute("role", "group");
      actions.setAttribute("aria-label", text.groupChoices + " " + group.name);
      for (const [value, label] of [["include", text.includeGroup], ["later", decisions.later], ["decline", decisions.decline]]) {
        const button = element("button", label, "choice-button group-choice");
        button.type = "button";
        button.value = value;
        button.dataset.decision = value;
        button.setAttribute("aria-pressed", "false");
        button.addEventListener("click", () => {
          // A second click clears the group's optional choices; required lines stay included.
          const next = button.getAttribute("aria-pressed") === "true" ? "unselected" : value;
          group.items.forEach((item, j) => {
            if (!item.required) choices[i + ":" + j] = next;
          });
          refreshGroup();
        });
        groupButtons.push(button);
        actions.append(button);
      }
      section.append(actions);
      if (group.items.some((item) => item.required)) {
        section.append(element("p", text.groupHint, "muted group-hint"));
      }
    }
    group.items.forEach((item, j) => {
      const key = i + ":" + j;
      const row = element("article", "", "item");
      row.dataset.choice = item.required ? "include" : choices[key] || "unselected";
      const head = element("div", "", "item-head");
      head.append(element("h3", item.name), element("span", money(item.amount_minor), "amount"));
      row.append(head);
      if (item.description) row.append(element("p", item.description, "copy muted"));
      if (item.required) {
        row.append(element("p", "✓ " + text.required, "badge"));
      } else if (response) {
        row.append(element("p", choices[key] && choices[key] !== "unselected" ? decisions[choices[key]] : decisions.unselected, "badge"));
      } else {
        const actions = element("div", "", "item-choices");
        actions.setAttribute("role", "group");
        actions.setAttribute("aria-label", text.itemChoices + " " + item.name);
        const buttons = [];
        for (const [value, icon] of [["include", "✓"], ["later", "◷"], ["decline", "×"]]) {
          const button = element("button", "", "choice-button");
          const symbol = element("span", icon, "choice-icon");
          symbol.setAttribute("aria-hidden", "true");
          button.append(symbol, element("span", decisions[value]));
          button.type = "button";
          button.value = value;
          button.dataset.decision = value;
          button.setAttribute("aria-pressed", "false");
          button.addEventListener("click", () => {
            choices[key] = choices[key] === value ? "unselected" : value;
            if (group.rule !== "any" && choices[key] === "include") {
              for (const other of controls) {
                if (other.key !== key && choices[other.key] === "include") choices[other.key] = "unselected";
              }
            }
            refreshGroup();
          });
          buttons.push(button);
          actions.append(button);
        }
        controls.push({key, row, buttons});
        row.append(actions);
      }
      section.append(row);
    });
    refreshGroup();
    scope.append(section);
  });

  if (content.terms) {
    const terms = element("section", "", "card stack");
    terms.append(element("h2", text.terms), element("p", content.terms, "copy muted"));
    scope.append(terms);
  }

  if (response) {
    summary.append(element("h2", text.outcomes[response.outcome]), total);
    summary.append(element("p", [response.name, date.format(new Date(response.created_at * 1000))].filter(Boolean).join(" · "), "muted"));
    if (response.note) summary.append(element("p", response.note, "copy"));
  } else {
    summary.append(element("h2", text.total), total, element("p", text.openUntil + " " + date.format(new Date(view.expires_at * 1000)), "muted"));
    const form = element("form", "", "stack");
    const name = element("input");
    name.autocomplete = "name";
    name.maxLength = 160;
    const nameLabel = element("label", text.name);
    nameLabel.append(name);
    const note = element("textarea");
    note.maxLength = 4000;
    const noteLabel = element("label", text.message);
    noteLabel.append(note);
    const error = element("p", "", "error");
    error.setAttribute("role", "alert");
    form.append(nameLabel, noteLabel, error);
    const actions = [
      ["accepted", text.accept, "primary"],
      ["changes_requested", text.requestChanges, ""],
      ["declined", text.declineProposal, ""],
    ];
    const buttons = actions.map(([outcome, label, style]) => {
      const button = element("button", label, style);
      button.value = outcome;
      button.type = "submit";
      form.append(button);
      return button;
    });
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const outcome = event.submitter?.value || "accepted";
      error.textContent = "";
      if (outcome === "accepted" && !name.value.trim()) {
        error.textContent = text.nameError;
        name.focus();
        return;
      }
      if (outcome === "accepted") {
        const missing = content.categories.find((group, i) => group.rule === "one_required" &&
          !group.items.some((item, j) => choices[i + ":" + j] === "include"));
        if (missing) { error.textContent = text.requiredChoice(missing.name); return; }
      }
      if (outcome === "changes_requested" && !note.value.trim()) {
        error.textContent = text.noteError;
        note.focus();
        return;
      }
      if (outcome === "declined" && !window.confirm(text.confirmDecline)) return;
      buttons.forEach((button) => { button.disabled = true; });
      try {
        render(await request("respond", {outcome, name: name.value, note: note.value, selection: choices}));
        root.focus();
      } catch (err) {
        error.textContent = err.message;
        buttons.forEach((button) => { button.disabled = false; });
      }
    });
    summary.append(form);
  }
  refreshTotal();
}

root.tabIndex = -1;
if (!token) {
  root.replaceChildren(element("h1", "Proposal link required"), element("p", "Open the full private link from the sender."));
} else {
  request("view", {}).then(render).catch((error) => {
    root.replaceChildren(element("h1", text.unavailable), element("p", error.message));
  });
}
