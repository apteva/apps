// Host component stand-ins: these tests exercise Storage's state and requests.
export function Card({children}:any){return <div>{children}</div>}
export function CardHeader({title,subtitle,action,status}:any){return <div><h2>{title}</h2><span>{subtitle}</span><span>{status?.label}</span>{action&&<a href={action.href}>{action.label}</a>}</div>}
export function DataList(){return null}
