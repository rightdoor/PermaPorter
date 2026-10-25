package database

// 清理任务的对账查询：Maven 仓库协议语义下清理只做「复位丢失本体的缓存状态、
// 删除无 DB 记录的孤立文件」，绝不删除构件记录与仅元数据构件
//（maven-metadata.xml 缓存合法且有用——版本区间/动态版本解析只读元数据即可工作）。
// 具体实现见 mvnadapter/cleanup（与 go/npm/py 清理器语义独立，不得通用化）。
