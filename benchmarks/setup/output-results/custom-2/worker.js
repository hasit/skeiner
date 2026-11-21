const spec=parseYaml(await Deno.readTextFile(Deno.env.get('SPEC')));
const base=new URL(Deno.env.get('BASE_URL')), token=Deno.env.get('TOKEN');
if(!token || !['http:','https:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) throw new Error('Invalid configuration');
function inlineSchema(node,seen=new Set()) {
 if(Array.isArray(node)) return node.map(v=>inlineSchema(v,seen));
 if(node===null || typeof node!=='object') return node;
 if(Object.hasOwn(node,'$ref')) {
  const ref=node.$ref;
  if(typeof ref!=='string'||!ref.startsWith('#/')||seen.has(ref)) throw new Error('Unsupported schema reference');
  let target=spec;
  for(const part of decodeURIComponent(ref.slice(2)).split('/')) { const key=part.replace(/~1/g,'/').replace(/~0/g,'~'); if(!target||!Object.hasOwn(target,key)) throw new Error('Missing schema reference'); target=target[key]; }
  return inlineSchema(target,new Set(seen).add(ref));
 }
 return Object.fromEntries(Object.entries(node).map(([k,v])=>[k,inlineSchema(v,seen)]));
}
const catalog=new Map();
for(const [path,name] of [['/vaults','GetVaults'],['/vaults/{vaultUuid}','GetVaultById']]) {
 const item=spec.paths[path], op=item?.get;
 if(op?.operationId!==name) throw new Error('Missing approved operation');
 const props=Object.create(null), required=[], params=new Map();
 for(const p of [...(item.parameters||[]),...(op.parameters||[])]) {
  if(p.schema?.type!=='string'||!(p.in==='query' || (path.endsWith('{vaultUuid}') && p.in==='path' && p.name==='vaultUuid'))) throw new Error('Unsupported parameter');
  params.set(p.name,p);
 }
 for(const [k,p] of params){ props[k]={type:'string',description:p.description,...(p.schema.pattern?{pattern:p.schema.pattern}:{})}; if(p.required || p.in==='path') required.push(k); }
 const responseSchema=inlineSchema(op.responses['200'].content['application/json'].schema);
 catalog.set(name,{path,params,tool:{name,description:op.summary,_meta:{responseSchema},inputSchema:{type:'object',properties:props,required,additionalProperties:false}}});
}
const server=new Server({name:'vault-metadata',version:'1.0.0'},{capabilities:{tools:{}}});
server.setRequestHandler(ListToolsRequestSchema,async()=>({tools:[...catalog.values()].map(x=>x.tool)}));
const fail=()=>({isError:true,content:[{type:'text',text:'Request rejected or unavailable.'}]});
server.setRequestHandler(CallToolRequestSchema,async req=>{try{
 const entry=catalog.get(req.params.name); if(!entry)return fail();
 const args=req.params.arguments??{};
 if(typeof args!=='object'||Array.isArray(args)||Object.keys(args).some(k=>!entry.params.has(k)))return fail();
 let path=entry.path; const u=new URL(base);
 for(const [k,p] of entry.params){if(!Object.hasOwn(args,k)){if(p.required||p.in==='path')return fail(); continue;} const v=args[k]; if(typeof v!=='string'||(p.schema.pattern&&!new RegExp(p.schema.pattern).test(v)))return fail(); if(p.in==='query')u.searchParams.append(k,v);else {if(!/^[a-z0-9]{26}$/.test(v))return fail();path=path.replace('{vaultUuid}',encodeURIComponent(v));}}
 u.pathname=base.pathname.replace(/\/+$/,'')+path;
 const res=await fetch(u,{method:'GET',redirect:'error',headers:{Authorization:'Bearer '+token,Accept:'application/json'}}); if(!res.ok)return fail(); const data=await res.json();
 if(entry.path==='/vaults'?!Array.isArray(data):(!data||typeof data!=='object'||Array.isArray(data)))return fail();
 const text=JSON.stringify(data,(k,v)=>{if(k.includes(token)||(typeof v==='string'&&v.includes(token)))throw new Error();return v;}); if(text.includes(token))return fail();return {content:[{type:'text',text}]};
}catch{return fail();}});
await server.connect(new StdioServerTransport());