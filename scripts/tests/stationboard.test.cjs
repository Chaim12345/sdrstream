'use strict';
const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');
class ClassList{constructor(){this.set=new Set();}add(c){this.set.add(c);}remove(c){this.set.delete(c);}toggle(c,force){const on=force===undefined?!this.set.has(c):force;on?this.set.add(c):this.set.delete(c);return on;}contains(c){return this.set.has(c);}}
class Element {
 constructor(tag){this.tagName=tag||'';this.children=[];this.value='';this.events={};this._text='';this.hidden=false;this.dataset={};this.attrs={};this.classList=new ClassList();}
 set textContent(v){this._text=String(v);this.children=[];}get textContent(){return this._text+this.children.map(c=>c.textContent).join('');}
 appendChild(c){this.children.push(c);return c;}
 setAttribute(k,v){this.attrs[k]=String(v);}getAttribute(k){return this.attrs[k];}
 addEventListener(k,f){(this.events[k] ||= []).push(f);}
 fire(k,ev){ev=ev||{};ev.preventDefault=ev.preventDefault||(()=>{});(this.events[k]||[]).forEach(f=>f(ev));}
}
function harness(){
 const registry=[];const byId={};
 const mk=(tag)=>{const e=new Element(tag);registry.push(e);return e;};
 const $=id=>byId[id] ||= mk();
 // Static filter chips from index.html.
 [['bkind',''],['bkind','kiwi'],['bkind','openwebrx'],['bkind','websdr'],
  ['breach',''],['breach','alive'],['breach','dead']].forEach(([k,v])=>{const c=mk('button');c.dataset[k]=v;if(!v)c.classList.add('on');});
 const calls=[],pending=[];
 // Static markup from index.html; the Go shell test pins the real text.
 $('board-disabled').textContent='Station probing is not enabled on this server. Start the server with --probe-directory to build the station catalog.';
 const document={
  getElementById:$,
  createElement:tag=>mk(tag),
  querySelectorAll:sel=>{const m=/\[data-(\w+)\]/.exec(sel);return m?registry.filter(e=>m[1] in e.dataset):[];},
 };
 const context={window:{},document,location:{hash:''},fmtDate:s=>'FMT('+s+')',
  fetchJSON:url=>{calls.push(url);return new Promise((resolve,reject)=>pending.push({resolve,reject}));}};
 vm.createContext(context);vm.runInContext(fs.readFileSync('go/internal/web/static/stationboard.js','utf8'),context);
 return {$,calls,pending,context,document};
}
const flush=()=>new Promise(r=>setImmediate(r));
const station=(over)=>Object.assign({key:'kiwi|a:8073',kind:'kiwi',name:'Test station',url:'http://a:8073',reachable:true,verdict:'ok',checked_at:'2026-10-25T05:00:00Z',capability:{}},over);
function openView(h){h.context.window.stationBoardRefresh();}

test('disabled catalog is a clear state, not an error',async()=>{
 const h=harness();openView(h);assert.deepEqual(h.calls,['/api/station-catalog.json']);
 h.pending.shift().resolve({enabled:false,stations:[]});await flush();
 assert.equal(h.$('board-disabled').hidden,false);
 assert.match(h.$('board-disabled').textContent,/probing is not enabled on this server/);
 assert.match(h.$('board-disabled').textContent,/--probe-directory/);
 assert.equal(h.$('board-enabled').hidden,true);
 assert.doesNotMatch(h.$('board-state').textContent,/unavailable|error|retry/i);
});

test('summary strip counts the unfiltered catalog; filters narrow the list only',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[
  station({key:'kiwi|a:8073',name:'Alive kiwi'}),
  station({key:'openwebrx|b:8073',kind:'openwebrx',name:'Dead owrx',reachable:false,verdict:'timeout',consec_failures:3}),
  station({key:'websdr|c:80',kind:'websdr',name:'Alive websdr'}),
 ]});await flush();
 assert.equal(h.$('board-total').textContent,'3');
 assert.equal(h.$('board-alive').textContent,'2');
 assert.equal(h.$('board-dead').textContent,'1');
 assert.match(h.$('board-updated').textContent,/FMT\(2026-10-25T05:30:00Z\)/);
 assert.equal(h.$('board-list').children.length,3);
 // Reachability filter: dead only; summary strip stays global.
 const dead=h.document.querySelectorAll('[data-breach]').find(c=>c.dataset.breach==='dead');
 dead.fire('click');
 assert.equal(h.$('board-list').children.length,1);
 assert.match(h.$('board-list').textContent,/Dead owrx/);
 assert.equal(h.$('board-total').textContent,'3');assert.equal(h.$('board-alive').textContent,'2');
 assert.equal(dead.classList.contains('on'),true);
 // Kind filter stacked on dead reachability: kiwi is alive, so nothing matches.
 const kiwi=h.document.querySelectorAll('[data-bkind]').find(c=>c.dataset.bkind==='kiwi');
 kiwi.fire('click');
 assert.equal(h.$('board-list').children.length,1);
 assert.match(h.$('board-list').textContent,/No station matches these filters/);
 // Back to all kinds: the dead openwebrx returns.
 const all=h.document.querySelectorAll('[data-bkind]').find(c=>c.dataset.bkind==='');
 all.fire('click');
 assert.match(h.$('board-list').textContent,/Dead owrx/);
});

test('card surfaces verdict, kind, coverage in MHz with bands, capacity, profiles and check state',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[
  station({name:'Haifa Kiwi',url:'http://kiwi.example:8073',verdict:'ok',consec_failures:0,
   capability:{freq_min_hz:10000,freq_max_hz:30000000,profiles:['20m','40m'],users_max:4,rx_channels:4,antenna:'MLA-30 loop',location:'Haifa, Israel',version:'v1.800',passworded:true}}),
  station({key:'websdr|c:80',kind:'websdr',name:'Dead WebSDR',url:'http://c:80',reachable:false,verdict:'refused',consec_failures:1,checked_at:'2026-10-25T04:00:00Z'}),
  station({key:'openwebrx|d:8073',kind:'openwebrx',name:'Single capacity',capability:{users_max:1,rx_channels:1}}),
 ]});await flush();
 const first=h.$('board-list').children[0];
 const text=first.textContent;
 assert.match(text,/Haifa Kiwi/);assert.match(text,/KiwiSDR/);assert.match(text,/alive · ok/);
 assert.match(text,/0\.01.30 MHz/);assert.match(text,/LF\/MF\/HF/);
 assert.match(text,/4 slots · 4 channels · password required/);
 assert.match(text,/profiles: 20m, 40m/);
 assert.match(text,/Haifa, Israel · MLA-30 loop/);
 assert.match(text,/checked FMT\(2026-10-25T05:00:00Z\)/);
 assert.match(text,/v1\.800/);
 assert.doesNotMatch(text,/consecutive failure/);
 const link=first.children.flatMap(c=>c.children).find(n=>n.tagName==='a') || first.children.find(n=>n.tagName==='a');
 assert.ok(link,'card links the station url');assert.equal(link.textContent,'http://kiwi.example:8073');
 const dead=h.$('board-list').children[1];
 assert.match(dead.textContent,/dead · refused/);assert.match(dead.textContent,/1 consecutive failure/);
 const single=h.$('board-list').children[2];
 assert.match(single.textContent,/1 slot · 1 channel/);assert.doesNotMatch(single.textContent,/1 slots|1 channels/);
});

test('unsafe station urls never become links; hostile markup stays inert text',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[
  station({name:'<img src=x onerror=bad>',url:'javascript:alert(1)'}),
 ]});await flush();
 const card=h.$('board-list').children[0];
 assert.match(card.textContent,/<img src=x onerror=bad>/);
 const anchors=[];(function walk(n){if(n.tagName==='a')anchors.push(n);n.children.forEach(walk);})(card);
 assert.equal(anchors.length,0,'javascript: url must not render as a link');
});

test('empty catalog differs from disabled; fetch failure keeps old data and offers retry',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[]});await flush();
 assert.match(h.$('board-list').textContent,/No stations in the catalog yet/);
 assert.equal(h.$('board-disabled').hidden,true);
 // Now a working catalog, then a failed refresh keeps the cards and reports the error.
 h.$('board-refresh').fire('click');
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[station({name:'Staying'})]});await flush();
 assert.match(h.$('board-list').textContent,/Staying/);
 h.$('board-refresh').fire('click');
 h.pending.shift().reject(new Error('HTTP 502'));await flush();
 assert.match(h.$('board-list').textContent,/Staying/);
 assert.match(h.$('board-state').textContent,/unavailable/);
 assert.match(h.$('board-state').textContent,/refresh to retry/);
});

test('a stale response cannot overwrite a newer one',async()=>{
 const h=harness();openView(h);openView(h); // second open within 60s must not refetch
 assert.equal(h.calls.length,1);
 h.$('board-refresh').fire('click');
 assert.equal(h.calls.length,2);
 h.pending[1].resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[station({name:'Newer'})]});await flush();
 h.pending[0].resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[station({name:'Stale'})]});await flush();
 assert.match(h.$('board-list').textContent,/Newer/);assert.doesNotMatch(h.$('board-list').textContent,/Stale/);
});

test('long catalogs page with show more',async()=>{
 const h=harness();openView(h);
 const many=[];for(let i=0;i<70;i++)many.push(station({key:'k|'+i,name:'Station '+i}));
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:many});await flush();
 assert.equal(h.$('board-list').children.length,60);
 assert.equal(h.$('board-more').hidden,false);
 assert.match(h.$('board-more').textContent,/10 more of 70/);
 h.$('board-more').fire('click');
 assert.equal(h.$('board-list').children.length,70);
 assert.equal(h.$('board-more').hidden,true);
});
test('activity section renders probe transitions with direction and verdict, never RF words',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[
  station({key:'kiwi|a:8073',name:'Haifa Kiwi'}),
  station({key:'websdr|gone:80',kind:'websdr',name:'Gone WebSDR',reachable:false,verdict:'timeout'}),
 ],events:[
  {key:'websdr|gone:80',at:'2026-10-25T05:10:00Z',kind:'unreachable',verdict:'timeout'},
  {key:'kiwi|a:8073',at:'2026-10-25T04:00:00Z',kind:'recovered'},
 ]});await flush();
 const box=h.$('board-events');
 assert.equal(box.children.length,2);
 const first=box.children[0];
 assert.match(first.textContent,/Gone WebSDR/);
 assert.match(first.textContent,/stopped answering/);
 assert.match(first.textContent,/FMT\(2026-10-25T05:10:00Z\)/);
 assert.match(first.textContent,/timeout/);
 const second=box.children[1];
 assert.match(second.textContent,/Haifa Kiwi/);
 assert.match(second.textContent,/resumed answering/);
 assert.doesNotMatch(box.textContent,/received|decoded|heard/i);
 // Newest first is the server's order; the UI must not re-sort.
 assert.ok(box.textContent.indexOf('Gone WebSDR')<box.textContent.indexOf('Haifa Kiwi'));
});

test('event for a station missing from the catalog falls back to its key',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[],events:[
  {key:'kiwi|old:8073',at:'2026-10-25T05:10:00Z',kind:'unreachable',verdict:'refused'},
 ]});await flush();
 assert.match(h.$('board-events').textContent,/kiwi\|old:8073/);
 assert.match(h.$('board-events').textContent,/stopped answering/);
});

test('no events yet is a quiet note, not an error',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[station()]});await flush();
 assert.match(h.$('board-events').textContent,/No reachability transitions recorded yet/);
 assert.doesNotMatch(h.$('board-state').textContent,/unavailable/);
});

test('next check line appears only when next_check_at is present',async()=>{
 const h=harness();openView(h);
 const soon=new Date(Date.now()+3*3600*1000).toISOString();
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[
  station({name:'With next',next_check_at:soon}),
  station({key:'websdr|b:80',kind:'websdr',name:'Without next'}),
 ],events:[]});await flush();
 const withNext=h.$('board-list').children[0];
 assert.match(withNext.textContent,/next check ~3h/);
 const without=h.$('board-list').children[1];
 assert.doesNotMatch(without.textContent,/next check/);
});
test('event key colliding with Object.prototype falls back to the key, never an inherited function',async()=>{
 const h=harness();openView(h);
 h.pending.shift().resolve({enabled:true,updated_at:'2026-10-25T05:30:00Z',stations:[station()],events:[
  {key:'constructor',at:'2026-10-25T05:10:00Z',kind:'unreachable',verdict:'timeout'},
 ]});await flush();
 const card=h.$('board-events').children[0];
 assert.match(card.textContent,/constructor/);
 assert.doesNotMatch(card.textContent,/function Object/);
});
