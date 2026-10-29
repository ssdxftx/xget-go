# User Instruction Memory

This file records user instructions, preferences, and teachings for reference in future interactions.

## Format

### User Instruction Entry
User instruction entries should follow this format:

[User Instruction Summary]
- Date: [YYYY-MM-DD]
- Context: [Mentioned scenario or time]
- Instructions:
  - [Content of user teaching or instruction, described line by line]

### Project Knowledge Entry
Entries discovered by the Agent during task execution should follow this format:

[Project Knowledge Summary]
- Date: [YYYY-MM-DD]
- Context: Discovered by Agent while performing [specific task description]
- Category: [Operations & Deployment|Build Methods|Testing Methods|Troubleshooting & Debugging|Workflow & Collaboration|Environment Configuration]
- Instructions:
  - [Specific knowledge points, described line by line]

## Deduplication Strategy
- Before adding a new entry, check for similar or identical instructions.
- If a duplicate is found, skip the new entry or merge it with the existing one.
- When merging, update the context or date information.
- This helps avoid redundant entries and keeps the memory file tidy.

## Entries

[User Instruction Summary]
- Date: 2026-10-27
- Context: 用户在 parity 对比审计过程中要求改用中文交流
- Instructions:
  - 所有回复、分析与结论输出统一使用简体中文

[User Instruction Summary]
- Date: 2026-10-29
- Context: 用户要求清理 Co-authored-by: monkeycode-ai 相关配置并固定推送身份
- Instructions:
  - 提交与推送统一使用 ssdxftx <1763610087@qq.com>
  - 禁止在提交信息中附加 Co-authored-by: monkeycode-ai monkeycode-ai@chaitin.com
