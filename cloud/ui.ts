export function el<K extends keyof HTMLElementTagNameMap>(tag: K, text = '', className = ''): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}

export function panel(title: string, description = ''): HTMLElement {
  const section = el('section', '', 'panel');
  section.setAttribute('aria-label', title);
  const header = el('div', '', 'panel-header');
  const titles = el('div');
  titles.append(el('h2', title, 'panel-title'));
  if (description) titles.append(el('p', description, 'panel-subtitle'));
  header.append(titles); section.append(header);
  return section;
}

export function table(name: string, columns: string[]): {wrapper: HTMLElement; body: HTMLTableSectionElement} {
  const wrapper = el('div', '', 'table-scroll');
  const node = el('table', '', 'data-table'); node.setAttribute('aria-label', name);
  const head = el('thead'), headings = el('tr');
  for (const column of columns) {const th = el('th', column); th.scope = 'col'; headings.append(th);}
  head.append(headings); const body = el('tbody'); node.append(head, body); wrapper.append(node);
  return {wrapper, body};
}

export function empty(title: string, description: string): HTMLElement {
  const node = el('div', '', 'empty-state');
  node.append(el('strong', title), el('p', description));
  return node;
}
