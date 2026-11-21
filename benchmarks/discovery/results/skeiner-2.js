const {owner,repo,tag}=api.input;
const read=(op,params)=>api.call(op,{path:{owner,repo,...params}});
const signed=obj=>Boolean(obj.verification.signature);
let {object}=await read('getTagReference',{tag});
const tag_chain=[];
while(object.type==='tag'){
  const node=await read('getAnnotatedTag',{tag_sha:object.sha});
  tag_chain.push({sha:node.sha,signed:signed(node)});
  object=node.object;
}
if(object.type!=='commit') throw new Error('Tag does not resolve to a commit');
const commit=await read('getGitCommit',{commit_sha:object.sha});
return {commit_sha:commit.sha,commit_subject:commit.message.split(/\r?\n/,1)[0],commit_signed:signed(commit),tag_chain};