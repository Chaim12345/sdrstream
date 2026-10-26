global.window={};require(process.argv[2]);const T=window.SDRTranslation;
const a=(x,y)=>{if(x!==y)throw new Error(JSON.stringify(x)+' != '+JSON.stringify(y))};
a(T.describe({kind:'data',detail:'x'}),null);
a(T.describe({kind:'voice',detail:''}).state,'no-transcript');
a(T.describe({kind:'voice',detail:'hola'}).state,'none');
const ok=T.describe({kind:'voice',detail:'hola',translation:{status:'translated',translated:'hello',provider:'gemini',model:'m1',source_lang:'es',machine_translated:true}});
a(ok.state,'translated');a(ok.text,'hello');a(ok.label,'Machine translation to English');
a(ok.meta[0],'by gemini m1');if(!/not verified/.test(ok.meta[1]))throw new Error('lang must be marked unverified');
a(T.describe({kind:'voice',detail:'x',translation:{status:'translated',translated:'y'}}).meta[1],'language not reported by provider');
for(const s of ['already_english','unchanged','disabled','ineligible','rate_limited','rejected','error']){
  const d=T.describe({kind:'voice',detail:'x',translation:{status:s,reason:'why',translated:'LEAK'}});
  a(d.text,'');a(d.state,s);if(!/why/.test(d.label))throw new Error('reason missing '+s);}
a(T.describe({kind:'voice',detail:'x',translation:{status:'translated',translated:''}}).text,'');
a(/mystery/.test(T.describe({kind:'voice',detail:'x',translation:{status:'mystery'}}).label),true);
console.log('translation ok');
