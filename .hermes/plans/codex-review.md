只读审查以下 diff，关注实际问题和性能影响，不要过度设计。

审查重点：
1. 复用路径（tryReuseSession + CheckReusable）是否有效率问题？CDP ping 和 JS eval 会不会太慢？
2. create mutex 会不会阻塞其他操作？
3. tombstone 机制是否合理？存太久浪费内存？
4. 代码是否过度设计？有没有不必要的抽象？
5. 初始化顺序修复是否真的解决了竞态？
6. 返回的状态契约（outcome/recommended_action）是否清晰？
7. 限流移到 service 内部后，handler 层是否还有必要保留限流？

输出格式：
- PASS — 没问题
- ISSUE: [文件:行号] 优先级(P0/P1/P2) 描述+建议

总体结论：PASS / MINOR / FAIL

不要提无关的建议，不要加测试代码，不要扩容 scope。
