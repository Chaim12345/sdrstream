'use strict';
const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');
class Element {
 constructor(){this.children=[];this.value='';this.events={};this._text='';this.hidden=false;}
 set textContent(v){this._text=String(v);this.children=[];}get textContent(){return this._text+this.children.map(c=>c.textContent).join('');}
 focus(){}getBoundingClientRect(){return {top:0,height:0};}
 appendChild(c){this.children.push(c);}setAttribute(){}addEventListener(k,f){this.events[k]=f;}
 fire(k){return this.events[k]?.({preventDefault(){}});}reset(){}
}
function harness(){const elements={};const $=id=>elements[id] ||= new Element();const calls=[],pending=[],landings=[];
 const context={window:{scrollY:0,scrollTo(options){landings.push(options);}},document:{getElementById:$,createElement:()=>new Element(),querySelector:()=>null},URLSearchParams,fmtDate:s=>s,
  setEmpty:(box,text)=>box.textContent=text,
  fetchJSON:url=>{calls.push(url);return new Promise((resolve,reject)=>pending.push({resolve,reject}));}};
 vm.createContext(context);vm.runInContext(fs.readFileSync('go/internal/web/static/history.js','utf8'),context);
 return {$,calls,pending,landings};
}
const flush=()=>new Promise(r=>setImmediate(r));
function page(items,total=items.length,offset=0,next=null){return {items,total,available:123,offset,next_offset:next,scope:'snapshot'};}
test('search encodes filters, renders source class as text, pages50 with original submitted filters',async()=>{
 const {$,calls,pending}=harness();$('history-query').value='CQ & <script>';$('history-class').value='synthetic';
 $('history-form').fire('submit');assert.match(calls[0],/q=CQ\+%26\+%3Cscript%3E/);
 pending.shift().resolve(page([{store:'decode',source_class:'synthetic',text:'<img onerror=bad>',name:'a.txt',url:'javascript:bad'}],123,0,50));await flush();
 assert.match($('history-results').textContent,/synthetic - not a reception/);assert.match($('history-results').textContent,/<img onerror=bad>/);
 assert.equal($('history-results').children[0].children.some(n=>n.href),false);
 $('history-query').value='unsent edit';$('history-next').fire('click');assert.match(calls[1],/offset=50/);assert.doesNotMatch(calls[1],/unsent/);
 pending.shift().resolve(page([],123,50));await flush();assert.equal($('history-prev').hidden,false);
});
test('late stale search cannot overwrite new results; error has retry message and no old results',async()=>{
 const {$,pending}=harness();$('history-form').fire('submit');$('history-form').fire('submit');
 pending[1].resolve(page([{store:'decode',source_class:'unknown',text:'new'}]));await flush();
 pending[0].resolve(page([{store:'decode',source_class:'foreign',text:'stale'}]));await flush();
 assert.match($('history-results').textContent,/new/);assert.doesNotMatch($('history-results').textContent,/stale/);
 $('history-form').fire('submit');pending[2].reject(Error('offline'));await flush();
 assert.equal($('history-results').textContent,'');assert.match($('history-state').textContent,/Search history to retry/);
});
test('no history differs from no matching filter',async()=>{
 const {$,pending}=harness();$('history-form').fire('submit');pending[0].resolve(page([]));await flush();
 assert.match($('history-results').textContent,/No saved record matches/);
 assert.match($('history-state').textContent,/^0 of 0 matching records/);
 assert.doesNotMatch($('history-state').textContent,/0-0/);
 $('history-form').fire('submit');pending[1].resolve({...page([]),available:0});await flush();
 assert.match($('history-results').textContent,/No capture, decode or gallery metadata/);
});
test('recorded rejection wins history card badge even with live source',async()=>{
 const {$,pending}=harness();$('history-form').fire('submit');
 pending[0].resolve(page([{store:'capture',source_class:'live-unverified',rejected:'specific failure'}]));await flush();
 assert.equal($('history-results').children[0].children[0].children[0].textContent,'recorded gate rejection: specific failure · live source - no recorded gate pass');
});

test('rejected synthetic record retains provenance badge beside rejection',async()=>{
 const {$,pending}=harness();$('history-form').fire('submit');
 pending[0].resolve(page([{store:'capture',source_class:'synthetic',rejected:'fixture failure'}]));await flush();
 const badges=$('history-results').children[0].children[0].children;
 assert.equal(badges.length,1);
 assert.equal(badges[0].textContent,'recorded gate rejection: fixture failure · test fixture / synthetic - not a reception');
 assert.equal(($('history-results').textContent.match(/fixture failure/g)||[]).length,1);
});

test('a successfully loaded page lands on focused summary; stale requests do not scroll',async()=>{
 const {$,pending,landings}=harness();let focused=0;$('history-state').focus=()=>focused++;
 $('history-form').fire('submit');$('history-form').fire('submit');
 pending[1].resolve(page([{store:'capture',source_class:'synthetic'}],62,50));await flush();
 assert.equal(focused,1);assert.equal(landings.length,1);assert.equal(landings[0].behavior,'auto');
 pending[0].resolve(page([],0));await flush();assert.equal(landings.length,1);
});
