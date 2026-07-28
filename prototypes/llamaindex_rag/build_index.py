from __future__ import annotations

try:
    from .config import PrototypeConfig
    from .corpus import load_documents
    from .runtime import build_index, configure_settings
    from .serve import read_index_metadata
except ImportError:
    from config import PrototypeConfig
    from corpus import load_documents
    from runtime import build_index, configure_settings
    from serve import read_index_metadata


def main() -> None:
    cfg = PrototypeConfig.load()
    cfg.validate()
    configure_settings(cfg)
    docs = load_documents(
        cfg.data_path,
        chunk_size=cfg.chunk_size,
        chunk_overlap=cfg.chunk_overlap,
    )
    index = build_index(cfg)
    metadata = read_index_metadata(cfg.persist_dir)
    print(f"Indexed {len(docs)} semantic chunks into {cfg.persist_dir}")
    print(f"Index type: {type(index).__name__}")
    print(f"Chunker version: {metadata.get('chunker_version', '')}")
    print(f"Built at UTC: {metadata.get('built_at_utc', '')}")
    print(f"Source markdown files: {metadata.get('source_markdown_file_count', 0)}")
    print(f"Supported providers: {metadata.get('supported_provider_count', 0)}")


if __name__ == "__main__":
    main()
