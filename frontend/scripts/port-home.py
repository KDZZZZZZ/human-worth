from html.parser import HTMLParser
from pathlib import Path
import json,re
class N:
 def __init__(self,tag,attrs=()): self.tag=tag;self.attrs=dict(attrs);self.children=[]
class Parser(HTMLParser):
 def __init__(self): super().__init__(convert_charrefs=True);self.root=N('root');self.stack=[self.root]
 def handle_starttag(self,t,a):
  n=N(t,a);self.stack[-1].children.append(n)
  if t not in ['img','br','input','meta','link','hr']: self.stack.append(n)
 def handle_endtag(self,t):
  for i in range(len(self.stack)-1,0,-1):
   if self.stack[i].tag==t:self.stack=self.stack[:i];break
 def handle_data(self,d): self.stack[-1].children.append(d)
def find(n,p):
 if isinstance(n,N):
  if p(n):return n
  for c in n.children:
   r=find(c,p)
   if r:return r
def jsx(n):
 if isinstance(n,str):return '{'+json.dumps(n,ensure_ascii=False)+'}' if n.strip() else ''
 attrs=[]
 for k,v in n.attrs.items():
  if k=='href' and v in ['discovery.html','submit.html']:v={'discovery.html':'/discovery','submit.html':'/submit'}[v]
  if k=='src' and v=='library-context.png':v='/media/library-context.png'
  k={'viewbox':'viewBox','class':'className','for':'htmlFor','tabindex':'tabIndex','fetchpriority':'fetchPriority','stroke-width':'strokeWidth'}.get(k,k)
  attrs.append(k+'='+json.dumps(v or '',ensure_ascii=False))
 start='<'+n.tag+(' '+' '.join(attrs) if attrs else '')
 if n.tag in ['img','br','input','meta','link','hr']:return start+'/>'
 return start+'>'+''.join(jsx(c) for c in n.children)+'</'+n.tag+'>'
s=Path('prototypes/frontend/index.html').read_text(encoding='utf-8');p=Parser();p.feed(s)
parts={'HomeOpening':lambda n:n.attrs.get('class')=='opening','HomeTransition':lambda n:n.attrs.get('id')=='transition','HomeExamples':lambda n:n.attrs.get('class')=='context-gallery','HomeInvitation':lambda n:n.attrs.get('class')=='invitation','LibraryBrief':lambda n:n.attrs.get('class')=='document library-brief','IssueBrief':lambda n:n.attrs.get('class')=='document issue-document','CodeBrief':lambda n:n.attrs.get('class')=='code-sheet'}
for name,predicate in parts.items():
 node=find(p.root,predicate);assert node,name
 Path('frontend/src/components/home/'+name+'.jsx').write_text('export default function '+name+'(){return ('+jsx(node)+');}\n',encoding='utf-8')
css='\n'.join(re.findall(r'<style>(.*?)</style>',s,re.S));css=css.replace(':root',':scope').replace('body{',':scope{')
Path('frontend/src/home-design.css').write_text('@scope (.home-site){\n'+css+'\n}\n.home-site main.page-width{padding:0;min-height:0}.home-site .nav-links button{font:inherit}\n',encoding='utf-8')
