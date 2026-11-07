const root = document.querySelector("#app");
const token = location.hash.slice(1);
const project = new URLSearchParams(location.search).get("project_id") || "";
const decisions = {
  unselected: "Choose an option", include: "Include", later: "For later", decline: "Decline",
};
const closed = {
  expired: "This proposal has expired.",
  withdrawn: "The sender withdrew this proposal.",
  superseded: "The sender replaced this revision. Ask them for the current link.",
};
const outcomes = {
  accepted: "Proposal accepted",
  declined: "Proposal declined",
  changes_requested: "Changes requested",
};

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
    method: "POST", credentials: "omit",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({token, ...body}),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || "The proposal could not be opened.");
  return data;
}

function render(view) {
  root.replaceChildren();
  if (!view.content) {
    root.append(element("h1", "Proposal unavailable"), element("p", closed[view.state] || "This proposal is closed."));
    return;
  }
  const {content, response} = view;
  const choices = {...response?.selection};
  const fmt = new Intl.NumberFormat(undefined, {style: "currency", currency: content.currency});
  const money = (minor) => fmt.format(minor / 100);
  const date = new Intl.DateTimeFormat(undefined, {dateStyle: "long"});
  const header = element("header", "", "hero");
  header.append(
    element("p", content.sender + " · Proposal", "eyebrow"),
    element("h1", content.title),
    element("p", [content.client_name && "Prepared for " + content.client_name, "Revision " + view.number].filter(Boolean).join(" · "), "muted"),
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
    const jump = element("button", "Review response");
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
    const rule = {any: "Choose any optional items", one: "Choose up to one item", one_required: "Choose one item to accept"};
    section.append(element("h2", group.name));
    if (group.items.some((item) => !item.required)) section.append(element("p", rule[group.rule], "muted"));
    const controls = [];
    group.items.forEach((item, j) => {
      const key = i + ":" + j;
      const row = element("article", "", "item");
      const head = element("div", "", "item-head");
      head.append(element("h3", item.name), element("span", money(item.amount_minor), "amount"));
      row.append(head);
      if (item.description) row.append(element("p", item.description, "copy muted"));
      if (item.required) {
        row.append(element("p", "Required", "badge"));
      } else if (response) {
        row.append(element("p", decisions[choices[key]] === decisions.unselected ? "No decision" : decisions[choices[key]], "badge"));
      } else {
        const label = element("label", "Your choice for " + item.name);
        const select = element("select");
        for (const [value, text] of Object.entries(decisions)) {
          const option = element("option", text);
          option.value = value;
          select.append(option);
        }
        select.value = choices[key] || "unselected";
        controls.push({key, select});
        select.addEventListener("change", () => {
          choices[key] = select.value;
          if (group.rule !== "any" && select.value === "include") {
            for (const other of controls) {
              if (other.key !== key && choices[other.key] === "include") {
                choices[other.key] = other.select.value = "unselected";
              }
            }
          }
          refreshTotal();
        });
        label.append(select);
        row.append(label);
      }
      section.append(row);
    });
    scope.append(section);
  });

  if (content.terms) {
    const terms = element("section", "", "card stack");
    terms.append(element("h2", "Terms"), element("p", content.terms, "copy muted"));
    scope.append(terms);
  }

  if (response) {
    summary.append(element("h2", outcomes[response.outcome]), total);
    summary.append(element("p", [response.name, date.format(new Date(response.created_at * 1000))].filter(Boolean).join(" · "), "muted"));
    if (response.note) summary.append(element("p", response.note, "copy"));
  } else {
    summary.append(element("h2", "Selected total"), total, element("p", "Open until " + date.format(new Date(view.expires_at * 1000)), "muted"));
    const form = element("form", "", "stack");
    const name = element("input");
    name.autocomplete = "name";
    name.maxLength = 160;
    const nameLabel = element("label", "Your name");
    nameLabel.append(name);
    const note = element("textarea");
    note.maxLength = 4000;
    const noteLabel = element("label", "Message for the sender");
    noteLabel.append(note);
    const error = element("p", "", "error");
    error.setAttribute("role", "alert");
    form.append(nameLabel, noteLabel, error);
    const actions = [
      ["accepted", "Accept selected scope", "primary"],
      ["changes_requested", "Request changes", ""],
      ["declined", "Decline proposal", ""],
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
        error.textContent = "Enter your name to accept.";
        name.focus();
        return;
      }
      if (outcome === "changes_requested" && !note.value.trim()) {
        error.textContent = "Describe what you would like changed.";
        note.focus();
        return;
      }
      if (outcome === "declined" && !window.confirm("Decline the entire proposal?")) return;
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
    root.replaceChildren(element("h1", "Proposal unavailable"), element("p", error.message));
  });
}
