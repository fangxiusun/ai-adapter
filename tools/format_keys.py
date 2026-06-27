"""
API Key 格式化工具

将各种格式的 API key 转换为 config.yaml 中 keys 配置的 YAML 格式。

支持的输入格式：
- 直接粘贴的 key 列表（每行一个 key）
- 已有 YAML 格式的 key 配置
- 包含 key 的任意文本

用法：
    python format_keys.py                    # 交互式输入
    python format_keys.py -f keys.txt        # 从文件读取
    python format_keys.py -k "tp-xxx" "sk-yyy"  # 命令行参数
    echo "tp-xxx" | python format_keys.py    # 从管道读取
"""

import re
import sys
import argparse
from typing import List, Dict
from collections import OrderedDict


# 支持的 key 模式：tp-, sk-, sk-ant-, ak- 开头，后跟至少20个字母数字字符
KEY_PATTERN = re.compile(r'(?:tp|sk|sk-ant|ak)-[a-zA-Z0-9]{20,}')


def extract_keys(text: str, deduplicate: bool = True) -> List[str]:
    """从文本中提取 API key
    
    Args:
        text: 输入文本
        deduplicate: 是否去重（默认 True）
    
    Returns:
        提取的 key 列表
    """
    if not text:
        return []
    
    keys = KEY_PATTERN.findall(text)
    
    if deduplicate:
        # 使用 OrderedDict 保持插入顺序并去重
        return list(OrderedDict.fromkeys(keys))
    
    return keys


def count_duplicates(text: str) -> Dict[str, int]:
    """统计重复的 key
    
    Returns:
        重复 key 及其出现次数的字典
    """
    if not text:
        return {}
    
    keys = KEY_PATTERN.findall(text)
    key_count = {}
    for key in keys:
        key_count[key] = key_count.get(key, 0) + 1
    
    # 只返回出现次数大于1的
    return {k: v for k, v in key_count.items() if v > 1}


def format_as_yaml(keys: List[str], indent: int = 6, start_index: int = 1) -> str:
    """将 key 列表格式化为 YAML 格式"""
    if not keys:
        return ""
    
    lines = []
    prefix = " " * indent
    for i, key in enumerate(keys, start_index):
        lines.append(f'{prefix}- value: "{key}"')
        lines.append(f'{prefix}  name: "key-{i}"')
    
    return "\n".join(lines)


def read_input(args) -> str:
    """读取输入内容"""
    # 从命令行参数读取
    if args.keys:
        return "\n".join(args.keys)
    
    # 从文件读取
    if args.file:
        try:
            with open(args.file, 'r', encoding='utf-8') as f:
                return f.read()
        except FileNotFoundError:
            print(f"错误：文件不存在 - {args.file}", file=sys.stderr)
            sys.exit(1)
        except Exception as e:
            print(f"错误：读取文件失败 - {e}", file=sys.stderr)
            sys.exit(1)
    
    # 检查是否有管道输入
    if not sys.stdin.isatty():
        return sys.stdin.read()
    
    # 交互式输入
    print("请输入 API key（每行一个，输入空行结束）：", file=sys.stderr)
    lines = []
    while True:
        try:
            line = input()
            if not line:
                break
            lines.append(line)
        except EOFError:
            break
    
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(
        description="API Key 格式化工具 - 将 key 转换为 config.yaml 格式",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例：
  %(prog)s                              # 交互式输入
  %(prog)s -f keys.txt                  # 从文件读取
  %(prog)s -k "tp-xxx" "sk-yyy"         # 命令行参数
  echo "tp-xxx" | %(prog)s              # 从管道读取
  %(prog)s -i 4                         # 从 key-4 开始编号
  %(prog)s --no-dedup                   # 不去重
        """
    )
    
    parser.add_argument(
        '-f', '--file',
        help='从文件读取 key'
    )
    parser.add_argument(
        '-k', '--keys',
        nargs='+',
        help='直接指定 key（空格分隔）'
    )
    parser.add_argument(
        '-i', '--start-index',
        type=int,
        default=1,
        help='起始编号（默认：1）'
    )
    parser.add_argument(
        '--indent',
        type=int,
        default=6,
        help='缩进空格数（默认：6）'
    )
    parser.add_argument(
        '--no-header',
        action='store_true',
        help='不输出 YAML keys 头部'
    )
    parser.add_argument(
        '--no-dedup',
        action='store_true',
        help='不去重（保留重复的 key）'
    )
    parser.add_argument(
        '--show-duplicates',
        action='store_true',
        help='显示重复的 key 统计'
    )
    parser.add_argument(
        '--keylen',
        type=int,
        default=0,
        help='合理key长度，为0 不检测（默认：0）'
    )
    
    args = parser.parse_args()
    
    # 读取输入
    text = read_input(args)
    
    # 统计重复
    if args.show_duplicates:
        duplicates = count_duplicates(text)
        if duplicates:
            print("重复的 key：", file=sys.stderr)
            for key, count in duplicates.items():
                print(f"  {key}: {count} 次", file=sys.stderr)
        else:
            print("没有重复的 key", file=sys.stderr)
    
    # 提取 key
    deduplicate = not args.no_dedup
    keys = extract_keys(text, deduplicate=deduplicate)
    
    if args.keylen:
        keys = [x.strip() for x in keys if len(x.strip()) == args.keylen]
    if not keys:
        print("警告：未找到有效的 API key", file=sys.stderr)
        print("支持的 key 格式：tp-xxx, sk-xxx, sk-ant-xxx, ak-xxx", file=sys.stderr)
        sys.exit(1)
    
    # 输出结果
    if not args.no_header:
        print("keys:")
    
    yaml_output = format_as_yaml(keys, indent=args.indent, start_index=args.start_index)
    print(yaml_output)
    
    # 统计信息
    total = len(KEY_PATTERN.findall(text))
    unique = len(keys)
    if deduplicate and total != unique:
        print(f"\n# 共提取 {unique} 个唯一 key（已去除 {total - unique} 个重复）", file=sys.stderr)
    else:
        print(f"\n# 共提取 {unique} 个 key", file=sys.stderr)


if __name__ == "__main__":
    main()
