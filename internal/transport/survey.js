'use strict';

const byId = (id) => document.getElementById(id);
const id = decodeURIComponent(window.location.pathname.split('/').pop());
const endpoint = new URL(`../../api/v1/surveys/${encodeURIComponent(id)}`, window.location.href);
const controls = new Map();
let survey;
let pages = [];
let pageIndex = 0;
let attempt;
let submitting = false;

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
function description(parent, text) {
  if (text) parent.append(element('p', text, 'question-description'));
}
function question(item) {
  const fieldset = element('fieldset', undefined, 'question');
  const legend = element('legend', item.title, 'question-title');
  if (item.required) legend.append(element('span', ' *', 'required'));
  fieldset.append(legend);
  description(fieldset, item.description);
  const entry = { item, fieldset, choices: [] };
  if (item.type === 'text') {
    const input = element('textarea');
    input.maxLength = 8000;
    input.name = item.id;
    input.setAttribute('aria-label', item.title);
    input.required = item.required;
    fieldset.append(input);
    entry.text = input;
  } else {
    for (const [index, option] of item.options.entries()) {
      const label = element('label', undefined, 'choice');
      const input = element('input');
      input.type = item.type === 'checkbox' ? 'checkbox' : 'radio';
      input.name = item.id;
      input.value = String(index);
      const text = element('span', option.label);
      if (option.description) text.append(element('small', option.description));
      label.append(input, text);
      fieldset.append(label);
      entry.choices.push({ input, value: option.label });
    }
    if (item.includeOther) {
      const label = element('label', undefined, 'choice');
      const other = element('input');
      other.type = item.type === 'checkbox' ? 'checkbox' : 'radio';
      other.name = item.id;
      other.value = 'other';
      label.append(other, element('span', 'Other'));
      const otherText = element('input', undefined, 'other-text');
      otherText.type = 'text';
      otherText.maxLength = 8000;
      otherText.placeholder = 'Tell us more…';
      otherText.setAttribute('aria-label', `Other answer for ${item.title}`);
      otherText.disabled = true;
      fieldset.append(label, otherText);
      entry.other = other;
      entry.otherText = otherText;
      const update = () => { otherText.disabled = !other.checked; };
      other.addEventListener('change', update);
      for (const choice of entry.choices) choice.input.addEventListener('change', update);
    }
  }
  controls.set(item.id, entry);
  return fieldset;
}
function showPage() {
  pages.forEach((page, index) => { page.node.hidden = index !== pageIndex; });
  byId('page-label').textContent = `Page ${pageIndex + 1} of ${pages.length}`;
  byId('question-count').textContent = `${controls.size} question${controls.size === 1 ? '' : 's'}`;
  byId('progress').max = pages.length;
  byId('progress').value = pageIndex + 1;
  byId('back').hidden = pageIndex === 0;
  byId('continue').hidden = pageIndex === pages.length - 1;
  byId('submit').hidden = pageIndex !== pages.length - 1;
  byId('answer-error').hidden = true;
}
function readAnswer(entry) {
  if (entry.text) return { values: entry.text.value.trim() ? [entry.text.value] : [] };
  const answer = { values: entry.choices.filter((choice) => choice.input.checked).map((choice) => choice.value) };
  if (entry.other?.checked) answer.other = entry.otherText.value.trim();
  return answer;
}
function validatePage(index) {
  for (const questionId of pages[index].questions) {
    const entry = controls.get(questionId);
    const answer = readAnswer(entry);
    const missing = entry.item.required && !answer.values.length && !answer.other;
    if (missing || (entry.other?.checked && !answer.other)) {
      byId('answer-error').textContent = `Please answer “${entry.item.title}”${entry.other?.checked ? ' and fill in your Other answer' : ''}.`;
      byId('answer-error').hidden = false;
      (entry.other?.checked ? entry.otherText : entry.text || entry.choices[0].input).focus();
      return false;
    }
  }
  return true;
}
function lock(locked) {
  for (const entry of controls.values()) entry.fieldset.disabled = locked;
  byId('back').disabled = locked;
  byId('continue').disabled = locked;
}
async function load() {
  byId('loading').hidden = false;
  byId('load-error').hidden = true;
  byId('reload').hidden = true;
  try {
    const response = await fetch(endpoint, { credentials: 'omit' });
    if (!response.ok) throw new Error(response.status === 404 ? 'This survey is not available. It may be closed or the link may be incorrect.' : 'We couldn’t load this survey. Please try again.');
    survey = await response.json();
    byId('survey-title').textContent = survey.title;
    byId('survey-description').textContent = survey.description;
    document.title = `${survey.title} · CenterSurvey`;
    const container = byId('pages');
    container.replaceChildren();
    pages = [];
    controls.clear();
    let page;
    const newPage = () => { page = { node: element('div'), questions: [] }; pages.push(page); container.append(page.node); };
    newPage();
    for (const item of survey.items) {
      if (item.type === 'page' || item.type === 'section') {
        if (item.type === 'page' && page.node.childNodes.length) newPage();
        const section = element('div', undefined, 'section-header');
        section.append(element('h2', item.title));
        if (item.description) section.append(element('p', item.description));
        page.node.append(section);
      } else {
        page.node.append(question(item));
        page.questions.push(item.id);
      }
    }
    pageIndex = 0;
    showPage();
    byId('survey-view').hidden = false;
  } catch (failure) {
    byId('load-error').textContent = failure instanceof TypeError ? 'Check your connection, then reload the survey.' : failure.message;
    byId('load-error').hidden = false;
    byId('reload').hidden = false;
  } finally {
    byId('loading').hidden = true;
  }
}
byId('continue').addEventListener('click', () => {
  if (validatePage(pageIndex)) { pageIndex++; showPage(); pages[pageIndex].node.scrollIntoView({ block: 'start' }); }
});
byId('back').addEventListener('click', () => { pageIndex--; showPage(); });
byId('reload').addEventListener('click', () => window.location.reload());
byId('survey-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (submitting) return;
  if (!attempt) {
    for (let index = 0; index < pages.length; index++) {
      pageIndex = index;
      showPage();
      if (!validatePage(index)) return;
    }
    const answers = {};
    for (const [questionId, entry] of controls) answers[questionId] = readAnswer(entry);
    const key = Array.from(crypto.getRandomValues(new Uint8Array(24)), (byte) => byte.toString(16).padStart(2, '0')).join('');
    attempt = { key, body: JSON.stringify({ revision: survey.revision, answers }) };
  }
  submitting = true;
  lock(true);
  byId('submit').disabled = true;
  byId('submit').textContent = 'Sending your answers…';
  byId('answer-error').hidden = true;
  try {
    const response = await fetch(new URL(`${endpoint.pathname}/responses`, endpoint), {
      method: 'POST', credentials: 'omit',
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': attempt.key },
      body: attempt.body,
    });
    if (!response.ok) {
      if (response.status === 400) { attempt = undefined; lock(false); throw new Error('Please check your answers. A required answer may be missing or too long.'); }
      if (response.status === 409 || response.status === 404) {
        byId('reload').hidden = false;
        throw new Error('This survey has changed or is no longer accepting responses. Reload to start again with the current survey.');
      }
      throw new Error('We couldn’t confirm your response. Tap Retry submission to safely try again.');
    }
    const receipt = await response.json();
    byId('receipt-id').textContent = receipt.id;
    byId('survey-view').hidden = true;
    byId('thanks').hidden = false;
    byId('thanks-title').focus();
    document.title = 'Thank you · CenterSurvey';
  } catch (failure) {
    byId('answer-error').textContent = failure instanceof TypeError ? 'Your response is unconfirmed. Check your connection, then tap Retry submission. It will not create a duplicate response.' : failure.message;
    byId('answer-error').hidden = false;
  } finally {
    submitting = false;
    byId('submit').disabled = false;
    byId('submit').textContent = attempt ? 'Retry submission ↗' : 'Send my answers ↗';
  }
});
load();
