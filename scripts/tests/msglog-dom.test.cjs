'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
// Small DOM adapter: exercise the production script and its actual event handlers.
class Node {
 constructor(){this.children=[];this.dataset={};this.listeners={};this.scrollTop=0;this.clientHeight=100;this.offsetHeight=20;this.offsetTop=0;this.value='';}
 set textContent(v){this.text=v;this.children=[];} get textContent(){return this.text||'';}
 get scrollHeight(){return this.children.length*20;}
 append(...ns){ns.forEach(n=>{n.offsetTop=this.children.length*20;this.children.push(n);});}
 appendChild(n){this.append(n);} before(){} after(){} setAttribute(){}
 addEventListener(k,f){(this.listeners[k] ||= []).push(f);}
 fire(k){for(const f of this.listeners[k]||[])f();}
 querySelectorAll(){return this.children.filter(n=>n.className.startsWith('msgline'));}
}
function setup(){
 const nodes={}; const node=id=>nodes[id] ||= new Node();
 const created=[];
 const document={getElementById:id=>created.find(n=>n.id===id)||node(id),createElement:()=>{const n=new Node();created.push(n);return n;}};
 const rows=Array.from({length:123},(_,i)=>({started:String(i),mode:i%2?'cw':'rtty',detail:'message '+i}));
 const c={document,window:{},msglogMode:'',msglogStuck:false,decodedItems:()=>rows,fmtTime:x=>x,mhz:x=>x,renderMsglog(){},msglogText(){}};
 // Original app.js scroll listener runs before enhancement listener.
 node('msglog').addEventListener('scroll',()=>{c.msglogStuck=node('msglog').scrollTop+100<node('msglog').scrollHeight-40;});
 vm.createContext(c);vm.runInContext(fs.readFileSync('go/internal/web/static/msglog.js','utf8'),c);
 return {c,node:id=>document.getElementById(id),rows,nodes,created};
}
test('first visible row and repeated occurrence stay fixed across 80 arrivals; older, resume',()=>{
 const {c,node,rows,created}=setup();const box=node('msglog');
 // Give two rows identical payload identities and re-render before pausing.
 rows[74]={...rows[73]};c.renderMsglog();
 box.scrollTop=20;box.fire('scroll');
 const keys=box.children.map(n=>n.dataset.messageKey);
 assert.notEqual(keys[0],keys[1]);
 for(let i=0;i<80;i++)rows.push({started:'new'+i,mode:'cw',detail:'arrival'});
 c.renderMsglog();assert.deepEqual(box.children.map(n=>n.dataset.messageKey),keys);
 assert.equal(box.scrollTop,20);assert.equal(c.msglogStuck,true);
 created.find(n=>n.className==='toolbar msglog-history-nav').children[0].fire('click');
 assert.equal(box.children.length,100);
 assert.equal(box.children[51].dataset.messageKey,keys[1]);
 c.msglogStuck=false;c.renderMsglog();assert.notEqual(box.children[0].dataset.messageKey,keys[0]);
});
test('search chips and exports use matching set; reset is honest50',()=>{
 const {c,node}=setup();const input=node('msglog-search');
 input.value='message 12';input.fire('input');
 assert.equal(node('msglog').children.length,4);
 assert.equal(c.msglogText().split('\n').length,4);
 assert.equal(node('msglog-modes').children[0].textContent,'all (4)');
 node('msglog-modes').children.find(b=>b.dataset.dmode==='cw').fire('click');
 assert.equal(node('msglog').children.length,1);assert.equal(c.msglogText().split('\n').length,1);
 input.value='absent';input.fire('input');assert.equal(c.msglogText(),'');
 c.msglogMode='';input.value='';input.fire('input');assert.equal(node('msglog').children.length,50);
});
