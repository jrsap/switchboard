import argparse
import getpass

from sqlalchemy import select

from .db import Base, make_engine, make_session_factory
from .models import ApiKey, Membership, Organization, User
from .security import generate_api_key, hash_api_key, hash_password
from .settings import Settings


def open_db(settings: Settings):
    engine = make_engine(settings.database_url)
    Base.metadata.create_all(engine)
    return make_session_factory(engine)()


def main():
    parser = argparse.ArgumentParser(prog="switchboard")
    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser("init-db")

    p = sub.add_parser("create-org")
    p.add_argument("--name", required=True)

    p = sub.add_parser("create-user")
    p.add_argument("--username", required=True)
    p.add_argument("--password")
    p.add_argument("--org", required=True)
    p.add_argument("--role", default="member", choices=["member", "admin"])

    p = sub.add_parser("create-key")
    p.add_argument("--username", required=True)
    p.add_argument("--org", required=True)
    p.add_argument("--name", default="default")

    p = sub.add_parser("list-keys")
    p.add_argument("--username", required=True)

    args = parser.parse_args()
    settings = Settings()
    db = open_db(settings)
    try:
        if args.command == "init-db":
            print("database ready")
        elif args.command == "create-org":
            db.add(Organization(name=args.name))
            db.commit()
            print(f"created organization '{args.name}'")
        elif args.command == "create-user":
            password = args.password or getpass.getpass("Password: ")
            org = db.execute(select(Organization).where(Organization.name == args.org)).scalar_one_or_none()
            if org is None:
                raise SystemExit(f"organization '{args.org}' not found")
            user = User(username=args.username, password_hash=hash_password(password))
            db.add(user)
            db.flush()
            db.add(Membership(user_id=user.id, organization_id=org.id, role=args.role))
            db.commit()
            print(f"created user '{args.username}' with role '{args.role}' in org '{args.org}'")
        elif args.command == "create-key":
            org = db.execute(select(Organization).where(Organization.name == args.org)).scalar_one_or_none()
            if org is None:
                raise SystemExit(f"organization '{args.org}' not found")
            user = db.execute(select(User).where(User.username == args.username)).scalar_one_or_none()
            if user is None:
                raise SystemExit(f"user '{args.username}' not found")
            membership = db.execute(
                select(Membership).where(
                    Membership.user_id == user.id, Membership.organization_id == org.id
                )
            ).scalar_one_or_none()
            if membership is None:
                raise SystemExit(f"user '{args.username}' is not a member of org '{args.org}'")
            raw_key = generate_api_key()
            db.add(
                ApiKey(
                    key_hash=hash_api_key(raw_key),
                    name=args.name,
                    user_id=user.id,
                    organization_id=org.id,
                )
            )
            db.commit()
            print(f"created api key '{args.name}' for '{args.username}' in org '{args.org}'")
            print(f"plaintext key (shown once): {raw_key}")
        elif args.command == "list-keys":
            user = db.execute(select(User).where(User.username == args.username)).scalar_one_or_none()
            if user is None:
                raise SystemExit(f"user '{args.username}' not found")
            keys = db.execute(select(ApiKey).where(ApiKey.user_id == user.id)).scalars().all()
            for key in keys:
                org = db.get(Organization, key.organization_id)
                print(f"{key.id}\t{key.name}\t{org.name}\tactive={key.is_active}\tcreated={key.created_at}")
    finally:
        db.close()


if __name__ == "__main__":
    main()
