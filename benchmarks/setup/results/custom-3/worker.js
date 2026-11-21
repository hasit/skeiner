const doc=parseYaml(await Deno.readTextFile(Deno.env.get('SPEC')));
const base=new URL(Deno.env.get('BASE_URL')), token=Deno.env.get('TOKEN');
if (!token || !['http:','https:'].includes(base.protocol) || base.search || base.hash || base.username || base.password) throw Error('Invalid configuration');
const catalog=new Map();
for(const [path,id] of [['/vaults','GetVaults'],['/vaults/{vaultUuid}','GetVaultById']]){
 const item=doc.paths[path],op=item.get;
 if(op.operationId!==id) throw Error('Unexpected operation');
 const params=new Map(),properties=Object.create(null),required=[];
 for(const p of [...(item.parameters??[]),...(op.parameters??[])]){
  if(p.schema?.type!=='string' || !(p.in==='query' || (p.in==='path' && path.endsWith('{vaultUuid}') && p.name==='vaultUuid'))) throw Error('Unsupported parameter');
  params.set(p.name,p);
 }
 for(const [key,p] of params){properties[key]={type:'string',...(p.description?{description:p.description}:{}),...(p.schema.pattern?{pattern:p.schema.pattern}:{})};if(p.required || p.in==='path')required.push(key);}
 catalog.set(id,{path,params,tool:{name:id,description:op.summary,inputSchema:{type:'object',properties,required,additionalProperties:false}}});
}
const server=new Server({name:'vault-metadata',version:'1.0.0'},{capabilities:{tools:{}}});
server.setRequestHandler(ListToolsRequestSchema,async()=>({tools:Array.from(catalog.values(),e=>e.tool)}));
const fail=message=>({isError:true,content:[{type:'text',text:message}]});
server.setRequestHandler(CallToolRequestSchema,async req=>{
 const entry=catalog.get(req.params.name);if(!entry)return fail('Unknown tool');
 try{
 const args=req.params.arguments??{};if(typeof args!=='object'||Array.isArray(args)||Object.keys(args).some(k=>!entry.params.has(k)))return fail('Invalid arguments');
 let path=entry.path;const u=new URL(base);
 for(const [k,p] of entry.params){if(!Object.hasOwn(args,k)){if(p.required||p.in==='path')return fail('Missing required argument');continue;}
 const v=args[k];if(typeof v!=='string'||(p.schema.pattern && !new RegExp(p.schema.pattern).test(v)))return fail('Invalid argument');
 if(p.in==='query')u.searchParams.set(k,v);else {if(!/^[a-z0-9]{26}$/.test(v))return fail('Invalid vault UUID');path=path.replace('{vaultUuid}',encodeURIComponent(v));}}
 u.pathname=base.pathname.replace(/\/+$/,'')+path;
 const r=await fetch(u,{method:'GET',redirect:'error',headers:{Authorization:'Bearer '+token,Accept:'application/json'}});if(!r.ok)return fail('Upstream request failed');
 const data=await r.json();if(entry.path==='/vaults'?!Array.isArray(data):(!data||typeof data!=='object'||Array.isArray(data)))return fail('Invalid upstream response');
 const containsSecret=x=>typeof x==='string'?x.includes(token):x && typeof x==='object' && Object.entries(x).some(([k,v])=>k.includes(token)||containsSecret(v));
 if(containsSecret(data))return fail('Unsafe upstream response');return {content:[{type:'text',text:JSON.stringify(data)}]};
 }catch{return fail('Request failed');}
});
await server.connect(new StdioServerTransport());