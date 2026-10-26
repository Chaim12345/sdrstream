/* English translation display for Activity cards.
 *
 * The server attaches an optional `translation` object next to the original
 * row (translate.Result): original, translated, source_lang, target_lang,
 * provider, model, status, reason, cached, machine_translated. Rules here:
 *  - the original text is always rendered first by the card, never replaced;
 *  - translated text appears only for status "translated", always labelled
 *    machine translation with provider/model and the language the provider
 *    REPORTED (unverified; may be empty);
 *  - every other status is stated plainly with its reason and no output text;
 *  - no translation object is not a status: a voice row without a transcript
 *    says there is no transcript (no speech-to-text text), a voice row with a
 *    transcript and no translation says none was recorded. Neither is
 *    presented as "already English" or as a failure.
 */
(function () {
  'use strict';
  var STATUS_TEXT = {
    already_english: 'judged to be English already - nothing sent for translation',
    unchanged: 'provider returned the text unchanged - no separate translation shown',
    disabled: 'English translation is off',
    ineligible: 'not sent for translation',
    rate_limited: 'translation not done (rate limited, nothing cached)',
    rejected: 'translation output failed a sanity check and is not shown',
    error: 'translation failed'
  };
  function langLabel(code) {
    return code ? 'language reported by provider: ' + code + ' (not verified)' : 'language not reported by provider';
  }
  // Returns {state, label, text, meta[]}; text only for state "translated".
  function describe(a) {
    var t = a && a.translation;
    if (!t) {
      if (a && a.kind === 'voice') {
        if (!a.detail) return { state: 'no-transcript', label: 'no speech transcript recorded (no speech-to-text text), so nothing to translate', text: '', meta: [] };
        return { state: 'none', label: 'no English translation recorded for this transcript', text: '', meta: [] };
      }
      return null;
    }
    if (t.status === 'translated' && t.translated) {
      var meta = [];
      var who = [t.provider, t.model].filter(Boolean).join(' ');
      meta.push(who ? 'by ' + who : 'provider not recorded');
      meta.push(langLabel(t.source_lang));
      if (t.cached) meta.push('cached result');
      return { state: 'translated', label: 'Machine translation to English', text: t.translated, meta: meta };
    }
    var base = STATUS_TEXT[t.status] || ('translation status: ' + (t.status || 'not recorded'));
    if (t.status === 'translated') base = 'translation marked done but returned no text';
    return { state: t.status || 'unknown', label: base + (t.reason ? ': ' + t.reason : ''), text: '', meta: [] };
  }
  function render(a) {
    var d = describe(a);
    if (!d) return null;
    var box = document.createElement('div');
    box.className = 'translation tr-' + d.state;
    var lab = document.createElement('div'); lab.className = 'tr-label muted'; lab.textContent = d.label;
    box.appendChild(lab);
    if (d.text) {
      var body = document.createElement('div'); body.className = 'detail tr-text'; body.textContent = d.text;
      body.lang = 'en';
      box.appendChild(body);
      var m = document.createElement('div'); m.className = 'muted tr-meta'; m.textContent = d.meta.join(' · ');
      box.appendChild(m);
    }
    return box;
  }
  window.SDRTranslation = { describe: describe, render: render };
})();
