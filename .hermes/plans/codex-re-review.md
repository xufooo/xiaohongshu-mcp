只读审查当前 diff，确认上一轮 6 个 ISSUE 是否已正确修复，有无引入新问题。

检查要点：
1. ISSUE 1: 限流→CloseAll 顺序是否调过来了？
2. ISSUE 2: ctx.Err() 检查是否到位？
3. ISSUE 3: opToken 是否持有到检查完成？JS eval 是否合并为一次？
4. ISSUE 4: saveTombstone 是否在 session.mu 锁下读字段？
5. ISSUE 5: tombstone 不再阻挡重建？
6. ISSUE 6: Session 改为指针、OperationSince omitempty 移除？

输出格式：
- PASS — 没问题
- ISSUE: [文件:行号] 优先级(P0/P1/P2) 描述

总体结论：PASS / MINOR / FAIL
