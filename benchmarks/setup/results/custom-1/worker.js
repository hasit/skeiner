const doc=parseYaml(await Deno.readTextFile(Deno.env.get('SPEC')));
const base=new URL(Deno.env.get('BASE_URL')), token=Deno.env.get('TOKEN');
if(!token || !['http:','https:'].includes(base.protocol) || base.search || base.hash || base.username || base.password) throw new Error('Invalid configuration');
const catalog=new Map();
for(const [route,name] of [['/vaults','GetVaults'],['/vaults/{vaultUuid}','GetVaultById']]) {
 const op=doc.paths[route].get;
 if(op.operationId!==name) throw new Error('Approved operation mismatch');
 const properties=Object.create(null),required=[],params=new Map();
 for(const p of op.parameters??[]) {
  if(p.schema?.type!=='string'||!(p.in==='query'||(p.in==='path'&&p.name==='vaultUuid'&&route.endsWith('{vaultUuid}')))) throw new Error('Unsupported parameter');
  properties[p.name]={type:'string',description:p.description??p.name,...(p.schema.pattern?{pattern:p.schema.pattern}:{})};
  if(p.required||p.in==='path') required.push(p.name);
  params.set(p.name,p);
 }
 catalog.set(name,{route,params,tool:{name,description:op.summary,inputSchema:{type:'object',properties,required,additionalProperties:false}}});
}
const server=new Server({name:'vault-metadata',version:'1.0.0'},{capabilities:{tools:{}}});
server.setRequestHandler(ListToolsRequestSchema,async()=>({tools:[...catalog.values()].map(x=>x.tool)}));
const error=()=>({isError:true,content:[{type:'text',text:'Request rejected or unavailable.'}]});
function sensitive(x){if(typeof x==='string')return x.includes(token);return !!x&&typeof x==='object'&&Object.entries(x).some(([k,v])=>k.includes(token)||sensitive(v));}
server.setRequestHandler(CallToolRequestSchema,async req=>{try{
 const entry=catalog.get(req.params.name);if(!entry)return error();
 const args=req.params.arguments??{};if(typeof args!=='object'||Array.isArray(args)||entry.tool.inputSchema.required.some(k=>!Object.hasOwn(args,k)))return error();
 const url=new URL(base.href);let path=entry.route;
 for(const [key,value] of Object.entries(args)){
  const p=entry.params.get(key);if(!p||typeof value!=='string'||(p.schema.pattern&&!new RegExp(p.schema.pattern).test(value)))return error();
  if(p.in==='query')url.searchParams.set(key,value);else {if(!/^[a-z0-9]{26}$/.test(value))return error();path=path.replace('{vaultUuid}',encodeURIComponent(value));}
 }
 url.pathname=base.pathname.replace(/\/+$/,'')+path;
 const res=await fetch(url,{method:'GET',headers:{Authorization:'Bearer '+token,Accept:'application/json'},redirect:'error'});if(!res.ok)return error();
 const data=await res.json();if(sensitive(data))return error();return {content:[{type:'text',text:JSON.stringify(data)}]};
}catch{return error();}});
await server.connect(new StdioServerTransport());